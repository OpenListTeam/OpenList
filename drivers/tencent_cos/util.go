package tencent_cos

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	log "github.com/sirupsen/logrus"
	"github.com/tencentyun/cos-go-sdk-v5"
)

// do others that not defined in Driver interface

func getKey(filePath string, dir bool) string {
	filePath = strings.TrimPrefix(filePath, "/")
	if filePath != "" && dir {
		filePath += "/"
	}
	return filePath
}

var defaultPlaceholderName = ".openlist"

func getPlaceholderName(placeholder string) string {
	if placeholder == "" {
		return defaultPlaceholderName
	}
	return placeholder
}

func (d *TencentCOS) listObjects(ctx context.Context, dirPath string, args model.ListArgs) ([]model.Obj, error) {
	prefix := getKey(dirPath, true)
	log.Debugf("list: %s", prefix)
	files := make([]model.Obj, 0)
	marker := ""
	for {
		opt := &cos.BucketGetOptions{
			Prefix:    prefix,
			Delimiter: "/",
			MaxKeys:   1000,
		}
		if marker != "" {
			opt.Marker = marker
		}
		result, _, err := d.client.Bucket.Get(ctx, opt)
		if err != nil {
			return nil, err
		}
		for _, object := range result.CommonPrefixes {
			name := path.Base(strings.TrimSuffix(object, "/"))
			file := model.Object{
				Path:     path.Join(dirPath, name),
				Name:     name,
				Modified: d.Modified,
				IsFolder: true,
			}
			files = append(files, &file)
		}
		for _, object := range result.Contents {
			name := path.Base(object.Key)
			if !args.S3ShowPlaceholder && (name == getPlaceholderName(d.Placeholder) || name == d.Placeholder) {
				continue
			}
			file := model.Object{
				Path:     path.Join(dirPath, name),
				Name:     name,
				Size:     object.Size,
				Modified: d.Modified,
			}
			// Parse LastModified if available
			if object.LastModified != "" {
				if t, err := parseTime(object.LastModified); err == nil {
					file.Modified = t
				}
			}
			files = append(files, &file)
		}
		if !result.IsTruncated {
			break
		}
		marker = result.NextMarker
	}
	return files, nil
}

func parseTime(s string) (time.Time, error) {
	// COS returns time in format: 2019-04-23T02:21:05.000Z
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", s); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("unable to parse time: " + s)
}

func (d *TencentCOS) copyObject(ctx context.Context, src string, dst string, isDir bool) error {
	if isDir {
		return d.copyDir(ctx, src, dst)
	}
	return d.copyFile(ctx, src, dst)
}

func (d *TencentCOS) copyFile(ctx context.Context, src string, dst string) error {
	srcKey := getKey(src, false)
	dstKey := getKey(dst, false)
	// sourceURL format: <BucketName-APPID>/<ObjectKey>
	sourceURL := d.Bucket + "/" + srcKey
	_, _, err := d.client.Object.Copy(ctx, dstKey, sourceURL, nil)
	return err
}

func (d *TencentCOS) copyDir(ctx context.Context, src string, dst string) error {
	objs, err := op.List(ctx, d, src, model.ListArgs{S3ShowPlaceholder: true})
	if err != nil {
		return err
	}
	for _, obj := range objs {
		cSrc := path.Join(src, obj.GetName())
		cDst := path.Join(dst, obj.GetName())
		if obj.IsDir() {
			err = d.copyDir(ctx, cSrc, cDst)
		} else {
			err = d.copyFile(ctx, cSrc, cDst)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *TencentCOS) removeDir(ctx context.Context, src string) error {
	objs, err := op.List(ctx, d, src, model.ListArgs{})
	if err != nil {
		return err
	}
	for _, obj := range objs {
		cSrc := path.Join(src, obj.GetName())
		if obj.IsDir() {
			err = d.removeDir(ctx, cSrc)
		} else {
			err = d.removeFile(cSrc)
		}
		if err != nil {
			return err
		}
	}
	_ = d.removeFile(path.Join(src, getPlaceholderName(d.Placeholder)))
	_ = d.removeFile(path.Join(src, d.Placeholder))
	return nil
}

func (d *TencentCOS) removeFile(src string) error {
	key := getKey(src, false)
	_, err := d.client.Object.Delete(context.Background(), key)
	return err
}
