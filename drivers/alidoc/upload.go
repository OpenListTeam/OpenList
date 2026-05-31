package alidoc

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	netutil "github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/google/uuid"
)

const (
	defaultAliDocMultipartThreshold = 16 * 1024 * 1024
	defaultAliDocPartSize           = 100 * 1024
	maxAliDocMultipartParts         = 10000
)

func (d *AliDoc) put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	if file == nil {
		return nil, fmt.Errorf("file is nil")
	}
	if up == nil {
		up = func(float64) {}
	}

	parentID := d.RootFolderID
	parentPath := "/"
	if dstDir != nil {
		if id := strings.TrimSpace(dstDir.GetID()); id != "" {
			parentID = id
		}
		if p := dstDir.GetPath(); p != "" {
			parentPath = p
		}
	}

	src, size, err := prepareAliDocUploadFile(file)
	if err != nil {
		return nil, err
	}

	useMultipart := size > defaultAliDocMultipartThreshold
	info, err := d.getUploadInfo(ctx, parentID, file.GetName(), size, useMultipart)
	if err != nil {
		return nil, err
	}
	if size > 0 {
		partSize := calcAliDocPartSize(size, info.Data.FileUploadProtocolConfig.MinPartSize)
		if size > partSize && !useMultipart {
			useMultipart = true
			info, err = d.getUploadInfo(ctx, parentID, file.GetName(), size, true)
			if err != nil {
				return nil, err
			}
		}
	}

	startedAt := time.Now()
	if useMultipart && size > 0 {
		err = d.multipartUpload(ctx, src, size, info, up)
	} else {
		err = d.singleUpload(ctx, src, size, info, up)
	}
	if err != nil {
		return nil, err
	}
	if err := d.commitUpload(ctx, parentID, file.GetName(), size, info.Data.UploadKey); err != nil {
		return nil, err
	}

	if obj, err := d.findUploadedObj(ctx, parentID, parentPath, file.GetName(), size, startedAt); err == nil && obj != nil {
		return obj, nil
	}

	return &Object{
		Object: model.Object{
			Path:     joinPath(parentPath, file.GetName()),
			Name:     file.GetName(),
			Size:     size,
			Modified: startedAt,
			Ctime:    startedAt,
		},
		DentryType: "file",
	}, nil
}

func prepareAliDocUploadFile(file model.FileStreamer) (model.File, int64, error) {
	size := file.GetSize()
	if src := file.GetFile(); src != nil && size >= 0 {
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return nil, 0, err
		}
		return src, size, nil
	}

	src, err := file.CacheFullAndWriter(nil, nil)
	if err != nil {
		return nil, 0, err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}
	size = file.GetSize()
	if size < 0 {
		cur, err := src.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, 0, err
		}
		end, err := src.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, 0, err
		}
		size = end
		if _, err := src.Seek(cur, io.SeekStart); err != nil {
			return nil, 0, err
		}
	}
	return src, size, nil
}

func (d *AliDoc) getUploadInfo(ctx context.Context, parentDentryUUID, name string, fileSize int64, multipart bool) (uploadInfoResp, error) {
	var result uploadInfoResp
	body := map[string]interface{}{
		"uploadType":         "STS_SIGNATURE",
		"supportUploadTypes": []string{"STS_SIGNATURE", "HTTP_TO_CENTER"},
		"parentDentryUuid":   parentDentryUUID,
		"fileSize":           fileSize,
		"name":               name,
		"multipart":          multipart,
	}
	resp, err := d.request(ctx).
		SetBody(body).
		SetResult(&result).
		SetError(&result).
		Post(apiBase + "/box/api/v2/file/uploadinfo")
	if err != nil {
		return result, err
	}
	if err := checkResp(resp, result.apiResp); err != nil {
		return result, err
	}
	if strings.TrimSpace(result.Data.STSSignatureInfo.Bucket) == "" {
		return result, fmt.Errorf("empty upload bucket")
	}
	return result, nil
}

func (d *AliDoc) commitUpload(ctx context.Context, parentDentryUUID, name string, fileSize int64, uploadKey string) error {
	uploadKey = strings.TrimSpace(uploadKey)
	if uploadKey == "" {
		return fmt.Errorf("empty upload key")
	}

	var result apiResp
	body := map[string]interface{}{
		"parentDentryUuid":      parentDentryUUID,
		"uploadKey":             uploadKey,
		"fileSize":              fileSize,
		"name":                  name,
		"toPrevDentryUuid":      nil,
		"toNextDentryUuid":      nil,
		"batchId":               uuid.NewString(),
		"batchUploadType":       1,
		"batchParentDentryUuid": parentDentryUUID,
	}
	resp, err := d.request(ctx).
		SetBody(body).
		SetResult(&result).
		SetError(&result).
		Post(apiBase + "/box/api/v2/file/commit")
	if err != nil {
		return err
	}
	return checkResp(resp, result)
}

func calcAliDocPartSize(fileSize, minPartSize int64) int64 {
	partSize := minPartSize
	if partSize <= 0 {
		partSize = defaultAliDocPartSize
	}
	if fileSize <= 0 {
		return partSize
	}
	minRequired := int64(math.Ceil(float64(fileSize) / maxAliDocMultipartParts))
	if minRequired > partSize {
		partSize = minRequired
	}
	return partSize
}

func (d *AliDoc) singleUpload(ctx context.Context, src model.File, size int64, info uploadInfoResp, up driver.UpdateProgress) error {
	bucket, objectKey, err := d.newOSSBucket(info)
	if err != nil {
		return err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := io.NewSectionReader(src, 0, size)
	err = bucket.PutObject(
		objectKey,
		driver.NewLimitedUploadStream(ctx, io.TeeReader(reader, driver.NewProgress(size, up))),
	)
	if err != nil {
		return err
	}
	up(100)
	return nil
}

func (d *AliDoc) multipartUpload(ctx context.Context, src model.File, size int64, info uploadInfoResp, up driver.UpdateProgress) error {
	bucket, objectKey, err := d.newOSSBucket(info)
	if err != nil {
		return err
	}

	imur, err := bucket.InitiateMultipartUpload(objectKey, oss.Sequential())
	if err != nil {
		return err
	}

	partSize := calcAliDocPartSize(size, info.Data.FileUploadProtocolConfig.MinPartSize)
	partNum := int((size + partSize - 1) / partSize)
	parts := make([]oss.UploadPart, 0, partNum)
	progress := driver.NewProgress(size, up)

	var offset int64
	for partNumber := 1; partNumber <= partNum; partNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		length := partSize
		if remain := size - offset; remain < length {
			length = remain
		}

		var part oss.UploadPart
		var uploadErr error
		for attempt := 0; attempt < 3; attempt++ {
			reader := io.NewSectionReader(src, offset, length)
			part, uploadErr = bucket.UploadPart(
				imur,
				driver.NewLimitedUploadStream(ctx, io.TeeReader(reader, progress)),
				length,
				partNumber,
			)
			if uploadErr == nil {
				break
			}
		}
		if uploadErr != nil {
			return uploadErr
		}
		parts = append(parts, part)
		offset += length
	}

	_, err = bucket.CompleteMultipartUpload(imur, parts)
	if err != nil {
		return err
	}
	up(100)
	return nil
}

func (d *AliDoc) newOSSBucket(info uploadInfoResp) (*oss.Bucket, string, error) {
	sts := info.Data.STSSignatureInfo
	objectKey := strings.TrimSpace(sts.ObjectKey)
	if objectKey == "" {
		objectKey = strings.TrimSpace(info.Data.UploadKey)
	}
	if objectKey == "" {
		return nil, "", fmt.Errorf("empty upload object key")
	}

	endpoint, useCname := pickAliDocOSSEndpoint(sts)
	if endpoint == "" {
		return nil, "", fmt.Errorf("empty upload endpoint")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}

	options := []oss.ClientOption{oss.SecurityToken(sts.AccessToken)}
	if useCname {
		options = append(options, oss.UseCname(true))
	}
	client, err := netutil.NewOSSClient(
		endpoint,
		sts.AccessKeyID,
		sts.AccessKeySecret,
		options...,
	)
	if err != nil {
		return nil, "", err
	}
	bucket, err := client.Bucket(sts.Bucket)
	if err != nil {
		return nil, "", err
	}
	return bucket, objectKey, nil
}

func pickAliDocOSSEndpoint(sts uploadSTSSignatureInfo) (endpoint string, useCname bool) {
	if endpoint = strings.TrimSpace(sts.EndPoint); endpoint != "" {
		return endpoint, false
	}
	if endpoint = strings.TrimSpace(sts.Cname); endpoint != "" {
		return endpoint, true
	}
	if endpoint = strings.TrimSpace(sts.AccelerateCname); endpoint != "" {
		return endpoint, true
	}
	return "", false
}

func (d *AliDoc) findUploadedObj(ctx context.Context, parentID, parentPath, name string, size int64, startedAt time.Time) (model.Obj, error) {
	for attempt := 0; attempt < 5; attempt++ {
		items, err := d.list(ctx, parentID)
		if err != nil {
			return nil, err
		}
		var (
			matched    dentry
			hasMatched bool
		)
		for i := range items {
			item := items[i]
			if item.DentryType != "file" || item.Name != name {
				continue
			}
			if size >= 0 && item.FileSize != size {
				continue
			}
			if !hasMatched || item.UpdatedTime > matched.UpdatedTime {
				matched = item
				hasMatched = true
			}
		}
		if hasMatched {
			obj := toObj(parentPath, matched)
			if !obj.ModTime().IsZero() && obj.ModTime().Before(startedAt.Add(-5*time.Second)) {
				// Keep polling briefly if only an older homonymous file is visible.
			} else {
				return obj, nil
			}
		}
		if attempt < 4 {
			timer := time.NewTimer(time.Duration(attempt+1) * 300 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, fmt.Errorf("uploaded object not found")
}
