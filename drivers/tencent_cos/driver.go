package tencent_cos

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	stdpath "path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/tencentyun/cos-go-sdk-v5"
)

type TencentCOS struct {
	model.Storage
	Addition
	client *cos.Client
	config driver.Config
}

func (d *TencentCOS) Config() driver.Config {
	return d.config
}

func (d *TencentCOS) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *TencentCOS) Init(ctx context.Context) error {
	bucketURL, err := cos.NewBucketURL(d.Bucket, d.Region, true)
	if err != nil {
		return errors.Wrap(err, "failed to create bucket URL")
	}
	baseURL := &cos.BaseURL{
		BucketURL: bucketURL,
	}
	transport := &cos.AuthorizationTransport{
		SecretID:  d.SecretID,
		SecretKey: d.SecretKey,
	}
	httpClient := &http.Client{
		Transport: transport,
	}
	d.client = cos.NewClient(baseURL, httpClient)
	return nil
}

func (d *TencentCOS) Drop(ctx context.Context) error {
	return nil
}

func (d *TencentCOS) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return d.listObjects(ctx, dir.GetPath(), args)
}

func (d *TencentCOS) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	key := getKey(file.GetPath(), false)
	fileName := stdpath.Base(key)

	var link model.Link

	if d.CustomHost != "" {
		// Use custom host for generating link
		u, err := d.client.Object.GetPresignedURL2(ctx, http.MethodGet, key, time.Hour*time.Duration(d.SignURLExpire), nil)
		if err != nil {
			return nil, errors.Wrap(err, "failed to generate presigned URL")
		}
		// Replace host with custom host
		parsedURL, err := url.Parse(d.CustomHost)
		if err == nil {
			u.Scheme = parsedURL.Scheme
			u.Host = parsedURL.Host
		}
		link.URL = u.String()
	} else {
		if common.ShouldProxy(d, fileName) {
			// For proxied files, we need to sign the request but return it through proxy
			u, err := d.client.Object.GetPresignedURL2(ctx, http.MethodGet, key, time.Hour*time.Duration(d.SignURLExpire), nil)
			if err != nil {
				return nil, errors.Wrap(err, "failed to generate presigned URL")
			}
			link.URL = u.String()
			link.Header = http.Header{}
		} else {
			u, err := d.client.Object.GetPresignedURL2(ctx, http.MethodGet, key, time.Hour*time.Duration(d.SignURLExpire), nil)
			if err != nil {
				return nil, errors.Wrap(err, "failed to generate presigned URL")
			}
			link.URL = u.String()
		}
	}
	return &link, nil
}

func (d *TencentCOS) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	return d.Put(ctx, &model.Object{
		Path: stdpath.Join(parentDir.GetPath(), dirName),
	}, &stream.FileStream{
		Obj: &model.Object{
			Name:     getPlaceholderName(d.Placeholder),
			Modified: time.Now(),
		},
		Reader:   bytes.NewReader([]byte{}),
		Mimetype: "application/octet-stream",
	}, func(float64) {})
}

func (d *TencentCOS) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	err := d.Copy(ctx, srcObj, dstDir)
	if err != nil {
		return err
	}
	return d.Remove(ctx, srcObj)
}

func (d *TencentCOS) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	err := d.copyObject(ctx, srcObj.GetPath(), stdpath.Join(stdpath.Dir(srcObj.GetPath()), newName), srcObj.IsDir())
	if err != nil {
		return err
	}
	return d.Remove(ctx, srcObj)
}

func (d *TencentCOS) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return d.copyObject(ctx, srcObj.GetPath(), stdpath.Join(dstDir.GetPath(), srcObj.GetName()), srcObj.IsDir())
}

func (d *TencentCOS) Remove(ctx context.Context, obj model.Obj) error {
	if obj.IsDir() {
		return d.removeDir(ctx, obj.GetPath())
	}
	return d.removeFile(obj.GetPath())
}

func (d *TencentCOS) Put(ctx context.Context, dstDir model.Obj, s model.FileStreamer, up driver.UpdateProgress) error {
	key := getKey(stdpath.Join(dstDir.GetPath(), s.GetName()), false)
	contentType := s.GetMimetype()
	log.Debugln("key:", key)

	opt := &cos.ObjectPutOptions{
		ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{
			ContentType: contentType,
		},
	}

	body := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
		Reader:         s,
		UpdateProgress: up,
	})

	_, err := d.client.Object.Put(ctx, key, body, opt)
	return err
}

func (d *TencentCOS) GetDirectUploadTools() []string {
	if !d.EnableDirectUpload {
		return nil
	}
	return []string{"HttpDirect"}
}

func (d *TencentCOS) GetDirectUploadInfo(ctx context.Context, _ string, dstDir model.Obj, fileName string, _ int64) (any, error) {
	if !d.EnableDirectUpload {
		return nil, errs.NotImplement
	}
	key := getKey(stdpath.Join(dstDir.GetPath(), fileName), false)
	u, err := d.client.Object.GetPresignedURL2(ctx, http.MethodPut, key, time.Hour*time.Duration(d.SignURLExpire), nil)
	if err != nil {
		return nil, err
	}
	return &model.HttpDirectUploadInfo{
		UploadURL: u.String(),
		Method:    "PUT",
	}, nil
}

// Get implements driver.Getter interface
func (d *TencentCOS) Get(ctx context.Context, path string) (model.Obj, error) {
	path = stdpath.Join(d.GetRootPath(), path)
	key := getKey(path, false)

	// Try to get object as a file using HeadObject
	resp, err := d.client.Object.Head(ctx, key, nil)
	if err == nil {
		fileName := stdpath.Base(path)
		obj := &model.Object{
			Name: fileName,
			Path: path,
		}
		// Parse Content-Length
		if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
			var size int64
			fmt.Sscanf(contentLength, "%d", &size)
			obj.Size = size
		}
		// Parse Last-Modified
		if lastModified := resp.Header.Get("Last-Modified"); lastModified != "" {
			if t, err := time.Parse(time.RFC1123, lastModified); err == nil {
				obj.Modified = t
			} else if t, err := time.Parse(time.RFC1123Z, lastModified); err == nil {
				obj.Modified = t
			}
		}
		return obj, nil
	}

	// If HeadObject fails, check if it's a directory by listing with prefix
	if cos.IsNotFoundError(err) {
		prefix := getKey(path, true)
		opt := &cos.BucketGetOptions{
			Prefix:    prefix,
			Delimiter: "/",
			MaxKeys:   1,
		}
		result, _, listErr := d.client.Bucket.Get(ctx, opt)
		if listErr != nil {
			return nil, errors.WithMessage(listErr, "failed to list objects with prefix")
		}
		if len(result.Contents) > 0 || len(result.CommonPrefixes) > 0 {
			dirName := stdpath.Base(path)
			return &model.Object{
				Name:     dirName,
				Modified: d.Modified,
				IsFolder: true,
				Path:     path,
			}, nil
		}
		return nil, errs.ObjectNotFound
	}

	return nil, errors.WithMessage(err, "failed to head object")
}

var _ driver.Driver = (*TencentCOS)(nil)
var _ driver.Getter = (*TencentCOS)(nil)
