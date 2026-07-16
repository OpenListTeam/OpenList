package s3

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
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type S3 struct {
	model.Storage
	Addition
	cfg                aws.Config
	client             *s3.Client
	linkClient         *s3.Client
	directUploadClient *s3.Client

	config driver.Config
	cron   *cron.Cron
}

func (d *S3) Config() driver.Config {
	return d.config
}

func (d *S3) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *S3) Init(ctx context.Context) error {
	if d.Region == "" {
		d.Region = "openlist"
	}
	if d.config.Name == "Doge" {
		// 多吉云每次临时生成的秘钥有效期为 2h，所以这里设置为 118 分钟重新生成一次
		d.cron = cron.NewCron(time.Minute * 118)
		d.cron.Do(func() {
			err := d.initSession()
			if err != nil {
				log.Errorln("Doge init session error:", err)
			}
			d.client = d.getClient(ClientTypeNormal)
			d.linkClient = d.getClient(ClientTypeLink)
			d.directUploadClient = d.getClient(ClientTypeDirectUpload)
		})
	}
	err := d.initSession()
	if err != nil {
		return err
	}
	d.client = d.getClient(ClientTypeNormal)
	d.linkClient = d.getClient(ClientTypeLink)
	d.directUploadClient = d.getClient(ClientTypeDirectUpload)
	return nil
}

func (d *S3) Drop(ctx context.Context) error {
	if d.cron != nil {
		d.cron.Stop()
	}
	return nil
}

func (d *S3) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if d.ListObjectVersion == "v2" {
		return d.listV2(ctx, dir.GetPath(), args)
	}
	return d.listV1(ctx, dir.GetPath(), args)
}

func (d *S3) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	path := getKey(file.GetPath(), false)
	fileName := stdpath.Base(path)
	input := &s3.GetObjectInput{
		Bucket: &d.Bucket,
		Key:    &path,
		//ResponseContentDisposition: &disposition,
	}

	if d.CustomHost == "" {
		disposition := fmt.Sprintf(`attachment; filename*=UTF-8''%s`, url.PathEscape(fileName))
		if d.AddFilenameToDisposition {
			disposition = utils.GenerateContentDisposition(fileName)
		}
		input.ResponseContentDisposition = &disposition
	}

	presignClient := s3.NewPresignClient(d.linkClient)
	if presignClient == nil {
		return nil, fmt.Errorf("failed to create PresignClient")
	}
	var link model.Link
	var err error
	if d.CustomHost != "" {
		if d.EnableCustomHostPresign {
			result, presignErr := presignClient.PresignGetObject(ctx, input, s3.WithPresignExpires(time.Hour*time.Duration(d.SignURLExpire)))
			if presignErr != nil {
				return nil, fmt.Errorf("failed to presign link URL: %w", presignErr)
			}
			link.URL = result.URL
		} else {
			// Use a long-lived presigned URL with the custom host
			// The middleware will set the custom host on the request
			result, presignErr := presignClient.PresignGetObject(ctx, input, s3.WithPresignExpires(365*24*time.Hour))
			if presignErr != nil {
				return nil, fmt.Errorf("failed to generate link URL: %w", presignErr)
			}
			link.URL = result.URL
		}
		if err != nil {
			return nil, fmt.Errorf("failed to generate link URL: %w", err)
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
			result, presignErr := presignClient.PresignGetObject(ctx, input, s3.WithPresignExpires(time.Hour*time.Duration(d.SignURLExpire)))
			if presignErr != nil {
				return nil, fmt.Errorf("failed to sign link URL: %w", presignErr)
			}
			link.URL = result.URL
		} else {
			result, presignErr := presignClient.PresignGetObject(ctx, input, s3.WithPresignExpires(time.Hour*time.Duration(d.SignURLExpire)))
			if presignErr != nil {
				return nil, fmt.Errorf("failed to presign link URL: %w", presignErr)
			}
			link.URL = result.URL
		}
	}
	if err != nil {
		return nil, err
	}
	return &link, nil
}

func (d *S3) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
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

func (d *S3) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	err := d.Copy(ctx, srcObj, dstDir)
	if err != nil {
		return err
	}
	return d.Remove(ctx, srcObj)
}

func (d *S3) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	err := d.copy(ctx, srcObj.GetPath(), stdpath.Join(stdpath.Dir(srcObj.GetPath()), newName), srcObj.IsDir())
	if err != nil {
		return err
	}
	return d.Remove(ctx, srcObj)
}

func (d *S3) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return d.copy(ctx, srcObj.GetPath(), stdpath.Join(dstDir.GetPath(), srcObj.GetName()), srcObj.IsDir())
}

func (d *S3) Remove(ctx context.Context, obj model.Obj) error {
	if obj.IsDir() {
		return d.removeDir(ctx, obj.GetPath())
	}
	return d.removeFile(ctx, obj.GetPath())
}

func (d *S3) Put(ctx context.Context, dstDir model.Obj, s model.FileStreamer, up driver.UpdateProgress) error {
	uploader := manager.NewUploader(d.client)
	if s.GetSize() > int64(manager.MaxUploadParts)*manager.DefaultUploadPartSize {
		uploader.PartSize = s.GetSize() / int64(manager.MaxUploadParts-1)
	}
	key := getKey(stdpath.Join(dstDir.GetPath(), s.GetName()), false)
	contentType := s.GetMimetype()
	log.Debugln("key:", key)
	input := &s3.PutObjectInput{
		Bucket: &d.Bucket,
		Key:    &key,
		Body: driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
			Reader:         s,
			UpdateProgress: up,
		}),
		ContentType: &contentType,
	}
	_, err := uploader.Upload(ctx, input)
	return err
}

func (d *S3) GetDirectUploadTools() []string {
	if !d.EnableDirectUpload {
		return nil
	}
	return []string{"HttpDirect"}
}

func (d *S3) GetDirectUploadInfo(ctx context.Context, _ string, dstDir model.Obj, fileName string, _ int64) (any, error) {
	if !d.EnableDirectUpload {
		return nil, errs.NotImplement
	}
	path := getKey(stdpath.Join(dstDir.GetPath(), fileName), false)
	presignClient := s3.NewPresignClient(d.directUploadClient)
	result, err := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: &d.Bucket,
		Key:    &path,
	}, s3.WithPresignExpires(time.Hour*time.Duration(d.SignURLExpire)))
	if err != nil {
		return nil, err
	}
	return &model.HttpDirectUploadInfo{
		UploadURL: result.URL,
		Method:    "PUT",
	}, nil
}

// implements driver.Getter interface
func (d *S3) Get(ctx context.Context, path string) (model.Obj, error) {
	// try to get object as a file using HeadObject
	rootPath := d.GetRootPath()
	// Avoid double-prepending root path when path already contains it.
	// This happens when obj.GetPath() from a previous Get call is passed back
	// to op.List → op.Get → d.Get.
	if rootPath != "/" && utils.IsSubPath(rootPath, path) {
		// path already includes the root prefix
	} else {
		path = stdpath.Join(rootPath, path)
	}
	key := getKey(path, false)
	headInput := &s3.HeadObjectInput{
		Bucket: &d.Bucket,
		Key:    &key,
	}
	headOutput, err := d.client.HeadObject(ctx, headInput)
	if err == nil {
		// Object exists as a file
		fileName := stdpath.Base(path)
		return &model.Object{
			Name:     fileName,
			Size:     *headOutput.ContentLength,
			Modified: *headOutput.LastModified,
			Path:     path,
		}, nil
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() != "NotFound" {
		return nil, errors.WithMessage(err, "failed to head object")
	}

	// If HeadObject fails with 404, check if it's a directory
	prefix := getKey(path, true)
	var contents []types.Object
	var commonPrefixes []types.CommonPrefix
	switch d.ListObjectVersion {
	case "v1":
		listInput := &s3.ListObjectsInput{
			Bucket:  &d.Bucket,
			Prefix:  &prefix,
			MaxKeys: aws.Int32(1),
		}
		listResult, err := d.client.ListObjects(ctx, listInput)
		if err != nil {
			return nil, errors.WithMessage(err, "failed to list objects with prefix")
		}
		contents = listResult.Contents
		commonPrefixes = listResult.CommonPrefixes
	case "v2":
		listInput := &s3.ListObjectsV2Input{
			Bucket:  &d.Bucket,
			Prefix:  &prefix,
			MaxKeys: aws.Int32(1),
		}
		listResult, err := d.client.ListObjectsV2(ctx, listInput)
		if err != nil {
			return nil, errors.WithMessage(err, "failed to list objects v2 with prefix")
		}
		contents = listResult.Contents
		commonPrefixes = listResult.CommonPrefixes
	default:
		return nil, fmt.Errorf("unsupported ListObjectVersion: %s", d.ListObjectVersion)
	}
	if len(contents) > 0 || len(commonPrefixes) > 0 {
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

var _ driver.Driver = (*S3)(nil)
var _ driver.Getter = (*S3)(nil)
