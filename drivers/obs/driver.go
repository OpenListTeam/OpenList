package obs

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/huaweicloud/huaweicloud-sdk-go-obs/obs"
	log "github.com/sirupsen/logrus"
)

type OBS struct {
	model.Storage
	Addition
	client             *obs.ObsClient
	linkClient         *obs.ObsClient
	directUploadClient *obs.ObsClient

	config driver.Config
}

func (d *OBS) Config() driver.Config {
	return d.config
}

func (d *OBS) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *OBS) Init(ctx context.Context) error {
	if d.Region == "" {
		d.Region = "cn-north-4"
	}
	if d.SignURLExpire == 0 {
		d.SignURLExpire = 4
	}

	// 创建主客户端
	client, err := d.createClient()
	if err != nil {
		return fmt.Errorf("failed to create main client: %w", err)
	}
	d.client = client

	// 创建linkClient（用于生成下载链接）
	linkClient, err := d.createLinkClient()
	if err != nil {
		return fmt.Errorf("failed to create link client: %w", err)
	}
	d.linkClient = linkClient

	// 创建directUploadClient（用于直接上传）
	if d.EnableDirectUpload {
		directUploadClient, err := d.createDirectUploadClient()
		if err != nil {
			return fmt.Errorf("failed to create direct upload client: %w", err)
		}
		d.directUploadClient = directUploadClient
	}

	return nil
}

func (d *OBS) Drop(ctx context.Context) error {
	// OBS SDK的客户端不需要显式关闭
	return nil
}

func (d *OBS) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if d.ListObjectVersion == "v2" {
		return d.listV2(dir.GetPath(), args)
	}
	return d.listV1(dir.GetPath(), args)
}

func (d *OBS) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	path := getKey(file.GetPath(), false)
	fileName := stdpath.Base(path)

	input := &obs.CreateSignedUrlInput{
		Bucket: d.Bucket,
		Key:    path,
		Method: obs.HttpMethodGet,
	}

	if d.CustomHost == "" {
		disposition := fmt.Sprintf(`attachment; filename*=UTF-8''%s`, url.PathEscape(fileName))
		if d.AddFilenameToDisposition {
			disposition = utils.GenerateContentDisposition(fileName)
		}
		input.QueryParams = map[string]string{
			"response-content-disposition": disposition,
		}
	}

	var link model.Link
	var err error

	if d.CustomHost != "" {
		if d.EnableCustomHostPresign {
			output, err := d.linkClient.CreateSignedUrl(input)
			if err != nil {
				return nil, fmt.Errorf("failed to create signed URL: %w", err)
			}
			link.URL = output.SignedUrl
		} else {
			// 构建URL
			scheme := "https"
			host := d.CustomHost
			if d.ForcePathStyle {
				link.URL = fmt.Sprintf("%s://%s/%s/%s", scheme, host, d.Bucket, path)
			} else {
				link.URL = fmt.Sprintf("%s://%s.%s/%s", scheme, d.Bucket, host, path)
			}
		}

		if d.RemoveBucket {
			parsedURL, parseErr := url.Parse(link.URL)
			if parseErr != nil {
				log.Errorf("Failed to parse URL for bucket removal: %v, URL: %s", parseErr, link.URL)
				return nil, fmt.Errorf("failed to parse URL for bucket removal: %w", parseErr)
			}

			path := parsedURL.Path
			bucketPrefix := "/" + d.Bucket
			if strings.HasPrefix(path, bucketPrefix) {
				path = strings.TrimPrefix(path, bucketPrefix)
				if path == "" {
					path = "/"
				}
				parsedURL.Path = path
				link.URL = parsedURL.String()
				log.Debugf("Removed bucket '%s' from URL path: %s -> %s", d.Bucket, bucketPrefix, path)
			} else {
				log.Warnf("URL path does not contain expected bucket prefix '%s': %s", bucketPrefix, path)
			}
		}
	} else {
		if common.ShouldProxy(d, fileName) {
			output, err := d.linkClient.CreateSignedUrl(input)
			if err != nil {
				return nil, fmt.Errorf("failed to create signed URL: %w", err)
			}
			link.URL = output.SignedUrl
		} else {
			output, err := d.linkClient.CreateSignedUrl(input)
			if err != nil {
				return nil, fmt.Errorf("failed to create signed URL: %w", err)
			}
			link.URL = output.SignedUrl
		}
	}

	if err != nil {
		return nil, err
	}
	return &link, nil
}

func (d *OBS) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
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

func (d *OBS) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	err := d.Copy(ctx, srcObj, dstDir)
	if err != nil {
		return err
	}
	return d.Remove(ctx, srcObj)
}

func (d *OBS) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	err := d.copy(ctx, srcObj.GetPath(), stdpath.Join(stdpath.Dir(srcObj.GetPath()), newName), srcObj.IsDir())
	if err != nil {
		return err
	}
	return d.Remove(ctx, srcObj)
}

func (d *OBS) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return d.copy(ctx, srcObj.GetPath(), stdpath.Join(dstDir.GetPath(), srcObj.GetName()), srcObj.IsDir())
}

func (d *OBS) Remove(ctx context.Context, obj model.Obj) error {
	if obj.IsDir() {
		return d.removeDir(ctx, obj.GetPath())
	}
	return d.removeFile(obj.GetPath())
}

func (d *OBS) Put(ctx context.Context, dstDir model.Obj, s model.FileStreamer, up driver.UpdateProgress) error {
	key := getKey(stdpath.Join(dstDir.GetPath(), s.GetName()), false)
	contentType := s.GetMimetype()
	log.Debugln("key:", key)

	// 使用PutObject直接上传
	input := &obs.PutObjectInput{}
	input.Bucket = d.Bucket
	input.Key = key
	input.Body = driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{Reader: s, UpdateProgress: up})
	input.ContentType = contentType
	_, err := d.client.PutObject(input)
	return err
}

func (d *OBS) GetDirectUploadTools() []string {
	if !d.EnableDirectUpload {
		return nil
	}
	return []string{"HttpDirect"}
}

func (d *OBS) GetDirectUploadInfo(ctx context.Context, _ string, dstDir model.Obj, fileName string, _ int64) (any, error) {
	if !d.EnableDirectUpload {
		return nil, errs.NotImplement
	}
	path := getKey(stdpath.Join(dstDir.GetPath(), fileName), false)

	input := &obs.CreateSignedUrlInput{
		Bucket: d.Bucket,
		Key:    path,
		Method: obs.HttpMethodPut,
	}

	output, err := d.directUploadClient.CreateSignedUrl(input)
	if err != nil {
		return nil, fmt.Errorf("failed to create signed URL for direct upload: %w", err)
	}

	return &model.HttpDirectUploadInfo{
		UploadURL: output.SignedUrl,
		Method:    "PUT",
	}, nil
}

var _ driver.Driver = (*OBS)(nil)
