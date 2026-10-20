package bunny_storage

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

type BunnyStorage struct {
	model.Storage
	Addition
	client   *resty.Client
	endpoint *url.URL
	cdnBase  *url.URL
}

func (d *BunnyStorage) Config() driver.Config {
	cfg := config
	if d.StorageZoneName != "" && d.CDNBaseURL == "" {
		cfg.OnlyProxy = true
		cfg.PreferProxy = true
	}
	if d.CDNTokenKey != "" && d.CDNTokenIncludeIP {
		cfg.LinkCacheMode = driver.LinkCacheIP
	}
	return cfg
}

func (d *BunnyStorage) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *BunnyStorage) Init(ctx context.Context) error {
	if d.RootFolderPath == "" {
		d.RootFolderPath = "/"
	}
	if d.Endpoint == "" {
		d.Endpoint = defaultEndpoint
	}
	if d.SignURLExpire <= 0 {
		d.SignURLExpire = 4
	}
	if d.CDNTokenMethod == "" {
		d.CDNTokenMethod = cdnTokenMethodSHA256
	}
	endpoint, err := normalizeBaseURL(d.Endpoint, defaultEndpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint: %w", err)
	}
	d.endpoint = endpoint
	if d.CDNBaseURL != "" {
		cdnBase, err := normalizeBaseURL(d.CDNBaseURL, "")
		if err != nil {
			return fmt.Errorf("invalid cdn_base_url: %w", err)
		}
		d.cdnBase = cdnBase
	}
	d.client = base.RestyClient
	if d.client == nil {
		d.client = base.NewRestyClient()
	}
	return nil
}

func (d *BunnyStorage) Drop(ctx context.Context) error {
	return nil
}

func (d *BunnyStorage) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	var items []bunnyObject
	resp, err := d.authRequest().
		SetContext(ctx).
		SetResult(&items).
		Get(d.storageURL(dir.GetPath(), true))
	if err != nil {
		return nil, err
	}
	if err := d.handleResponseError(resp); err != nil {
		return nil, err
	}
	result := make([]model.Obj, 0, len(items))
	placeholder := d.placeholderName()
	for _, item := range items {
		if item.ObjectName == "" {
			continue
		}
		if !args.S3ShowPlaceholder && !item.IsDirectory && item.ObjectName == placeholder {
			continue
		}
		result = append(result, d.toObj(dir.GetPath(), item))
	}
	return result, nil
}

func (d *BunnyStorage) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if file.IsDir() {
		return nil, errs.NotFile
	}
	cacheTTL := time.Duration(0)
	if d.cdnBase != nil {
		linkURL := d.cdnURL(d.cdnObjectPath(file.GetPath()))
		link := &model.Link{
			URL:           linkURL,
			ContentLength: file.GetSize(),
			Expiration:    &cacheTTL,
		}
		if d.CDNTokenKey != "" {
			signedURL, _, err := d.signCDNURL(linkURL, args.IP)
			if err != nil {
				return nil, err
			}
			link.URL = signedURL
		}
		return link, nil
	}
	return &model.Link{
		URL:           d.storageURL(file.GetPath(), false),
		Header:        http.Header{"AccessKey": []string{d.AccessKey}},
		ContentLength: file.GetSize(),
		Expiration:    &cacheTTL,
	}, nil
}

func (d *BunnyStorage) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	dirPath := stdpath.Join(parentDir.GetPath(), dirName)
	placeholderPath := stdpath.Join(dirPath, d.placeholderName())
	if err := d.putReader(ctx, placeholderPath, bytes.NewReader(nil), 0, "application/octet-stream", nil); err != nil {
		return nil, err
	}
	now := time.Now()
	return &model.Object{
		Path:     dirPath,
		Name:     dirName,
		Modified: now,
		Ctime:    now,
		IsFolder: true,
	}, nil
}

func (d *BunnyStorage) Remove(ctx context.Context, obj model.Obj) error {
	resp, err := d.authRequest().
		SetContext(ctx).
		Delete(d.storageURL(obj.GetPath(), obj.IsDir()))
	if err != nil {
		return err
	}
	return d.handleResponseError(resp)
}

func (d *BunnyStorage) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	if up == nil {
		up = func(float64) {}
	}
	dstPath := stdpath.Join(dstDir.GetPath(), file.GetName())
	err := d.putReader(ctx, dstPath, driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
		Reader:         file,
		UpdateProgress: up,
	}), file.GetSize(), file.GetMimetype(), nil)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &model.Object{
		Path:     dstPath,
		Name:     file.GetName(),
		Size:     file.GetSize(),
		Modified: now,
		Ctime:    now,
	}, nil
}

func (d *BunnyStorage) putReader(ctx context.Context, path string, body any, size int64, contentType string, extraHeaders http.Header) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req := d.authRequest().
		SetContext(ctx).
		SetBody(body).
		SetHeader("Content-Type", contentType)
	if size >= 0 {
		req.SetHeader("Content-Length", fmt.Sprint(size))
	}
	for key, values := range extraHeaders {
		for _, value := range values {
			req.SetHeader(key, value)
		}
	}
	resp, err := req.Put(d.storageURL(path, false))
	if err != nil {
		return err
	}
	return d.handleResponseError(resp)
}

func (d *BunnyStorage) Get(ctx context.Context, path string) (model.Obj, error) {
	fullPath := stdpath.Join(d.GetRootPath(), path)
	parentPath, name := stdpath.Split(fullPath)
	parentPath = strings.TrimSuffix(parentPath, "/")
	if parentPath == "" {
		parentPath = "/"
	}
	objs, err := d.List(ctx, &model.Object{Path: parentPath, IsFolder: true}, model.ListArgs{S3ShowPlaceholder: true})
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		if obj.GetName() == name {
			return obj, nil
		}
	}
	return nil, errs.ObjectNotFound
}

var _ driver.Driver = (*BunnyStorage)(nil)
var _ driver.Getter = (*BunnyStorage)(nil)
