package obs

import (
	"context"
	"fmt"
	"net/http"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/huaweicloud/huaweicloud-sdk-go-obs/obs"
)

// getKey 处理路径格式，去除开头的/
func getKey(path string, isDir bool) string {
	path = strings.TrimPrefix(path, "/")
	if isDir && path != "" && !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path
}

// getPlaceholderName 获取占位文件名
func getPlaceholderName(placeholder string) string {
	if placeholder == "" {
		return ".placeholder"
	}
	return placeholder
}

// obsObjectToObj 将OBS对象元数据转换为model.Obj
func obsObjectToObj(object obs.Content) model.Obj {
	isDir := strings.HasSuffix(object.Key, "/")
	name := stdpath.Base(object.Key)
	if isDir && name == "" {
		name = stdpath.Base(strings.TrimSuffix(object.Key, "/"))
	}
	return &model.Object{
		Name:     name,
		Size:     object.Size,
		Modified: object.LastModified,
		IsFolder: isDir,
		Path:     "/" + object.Key,
	}
}

// obsPrefixToObj 将OBS前缀转换为目录对象
func obsPrefixToObj(prefix string) model.Obj {
	key := strings.TrimSuffix(prefix, "/")
	name := stdpath.Base(key)
	return &model.Object{
		Name:     name,
		Size:     0,
		Modified: time.Time{},
		IsFolder: true,
		Path:     "/" + prefix,
	}
}

// createClient 创建主客户端
func (d *OBS) createClient() (*obs.ObsClient, error) {
	ak := d.AccessKeyID
	sk := d.SecretAccessKey
	endpoint := d.Endpoint

	// 使用WithPathStyle配置路径风格
	if d.ForcePathStyle {
		return obs.New(ak, sk, endpoint, obs.WithPathStyle(true))
	}
	return obs.New(ak, sk, endpoint)
}

// createLinkClient 创建用于生成下载链接的客户端
func (d *OBS) createLinkClient() (*obs.ObsClient, error) {
	ak := d.AccessKeyID
	sk := d.SecretAccessKey
	endpoint := d.Endpoint

	if d.CustomHost != "" {
		endpoint = d.CustomHost
	}

	if d.ForcePathStyle {
		return obs.New(ak, sk, endpoint, obs.WithPathStyle(true))
	}
	return obs.New(ak, sk, endpoint)
}

// createDirectUploadClient 创建用于直接上传的客户端
func (d *OBS) createDirectUploadClient() (*obs.ObsClient, error) {
	ak := d.AccessKeyID
	sk := d.SecretAccessKey
	endpoint := d.Endpoint

	if d.DirectUploadHost != "" {
		endpoint = d.DirectUploadHost
	}

	if d.ForcePathStyle {
		return obs.New(ak, sk, endpoint, obs.WithPathStyle(true))
	}
	return obs.New(ak, sk, endpoint)
}

// listV1 使用v1 API列举对象
func (d *OBS) listV1(path string, args model.ListArgs) ([]model.Obj, error) {
	prefix := getKey(path, true)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	input := &obs.ListObjectsInput{
		Bucket: d.Bucket,
	}
	input.Prefix = prefix
	input.Delimiter = "/"

	var res []model.Obj

	for {
		output, err := d.client.ListObjects(input)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		// 处理目录
		for _, prefix := range output.CommonPrefixes {
			res = append(res, obsPrefixToObj(prefix))
		}

		// 处理文件
		for _, content := range output.Contents {
			// 跳过目录标记对象
			if strings.HasSuffix(content.Key, "/") && content.Size == 0 {
				continue
			}
			res = append(res, obsObjectToObj(content))
		}

		// 检查是否还有更多结果
		if !output.IsTruncated {
			break
		}
		input.Marker = output.NextMarker
	}

	return res, nil
}

// listV2 使用v2 API列举对象（OBS SDK v3不支持ListObjectsV2，使用ListObjects代替）
func (d *OBS) listV2(path string, args model.ListArgs) ([]model.Obj, error) {
	// OBS SDK v3不支持ListObjectsV2，回退到v1
	return d.listV1(path, args)
}

// removeFile 删除单个文件
func (d *OBS) removeFile(path string) error {
	key := getKey(path, false)
	input := &obs.DeleteObjectInput{
		Bucket: d.Bucket,
		Key:    key,
	}
	_, err := d.client.DeleteObject(input)
	return err
}

// removeDir 递归删除目录
func (d *OBS) removeDir(ctx context.Context, path string) error {
	objs, err := op.List(ctx, d, path, model.ListArgs{})
	if err != nil {
		return err
	}
	for _, obj := range objs {
		cSrc := stdpath.Join(path, obj.GetName())
		if obj.IsDir() {
			err = d.removeDir(ctx, cSrc)
		} else {
			err = d.removeFile(cSrc)
		}
		if err != nil {
			return err
		}
	}
	_ = d.removeFile(stdpath.Join(path, getPlaceholderName(d.Placeholder)))
	_ = d.removeFile(stdpath.Join(path, d.Placeholder))
	return nil
}

// copy 复制对象
func (d *OBS) copy(ctx context.Context, srcPath, dstPath string, isDir bool) error {
	if isDir {
		return d.copyDir(ctx, srcPath, dstPath)
	}
	return d.copyFile(ctx, srcPath, dstPath)
}

// copyFile 复制单个文件
func (d *OBS) copyFile(ctx context.Context, src string, dst string) error {
	srcKey := getKey(src, false)
	dstKey := getKey(dst, false)

	copyInput := &obs.CopyObjectInput{}
	copyInput.Bucket = d.Bucket
	copyInput.Key = dstKey
	copyInput.CopySourceBucket = d.Bucket
	copyInput.CopySourceKey = srcKey
	_, err := d.client.CopyObject(copyInput)
	return err
}

// copyDir 复制目录
func (d *OBS) copyDir(ctx context.Context, src string, dst string) error {
	objs, err := op.List(ctx, d, src, model.ListArgs{S3ShowPlaceholder: true})
	if err != nil {
		return err
	}
	for _, obj := range objs {
		cSrc := stdpath.Join(src, obj.GetName())
		cDst := stdpath.Join(dst, obj.GetName())
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

// handleError 处理OBS错误
func handleError(err error) error {
	if err == nil {
		return nil
	}

	// 检查是否为OBS错误
	if obsError, ok := err.(obs.ObsError); ok {
		switch obsError.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("object not found: %s", obsError.Message)
		case http.StatusForbidden:
			return fmt.Errorf("access denied: %s", obsError.Message)
		case http.StatusBadRequest:
			return fmt.Errorf("bad request: %s", obsError.Message)
		default:
			return fmt.Errorf("OBS error (status %d): %s", obsError.StatusCode, obsError.Message)
		}
	}

	return err
}
