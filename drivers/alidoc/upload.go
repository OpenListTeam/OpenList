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
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/avast/retry-go"
	"github.com/google/uuid"
)

const (
	defaultAliDocMultipartThreshold = 16 * 1024 * 1024
	defaultAliDocPartSize           = 100 * 1024
	maxAliDocMultipartParts         = 10000
)

func (d *AliDoc) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	size := file.GetSize()
	useMultipart := size > defaultAliDocMultipartThreshold
	info, err := d.getUploadInfo(ctx, dstDir.GetID(), file.GetName(), size, useMultipart)
	if err != nil {
		return err
	}
	if size > 0 {
		partSize := calcAliDocPartSize(size, info.Data.FileUploadProtocolConfig.MinPartSize)
		if size > partSize && !useMultipart {
			useMultipart = true
			info, err = d.getUploadInfo(ctx, dstDir.GetID(), file.GetName(), size, true)
			if err != nil {
				return err
			}
		}
	}

	if useMultipart {
		err = d.multipartUpload(ctx, file, size, info, up)
	} else {
		err = d.singleUpload(ctx, file, size, info, up)
	}
	if err != nil {
		return err
	}
	if err := d.commitUpload(ctx, dstDir.GetID(), file.GetName(), size, info.Data.UploadKey); err != nil {
		return err
	}
	return nil
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

func (d *AliDoc) singleUpload(ctx context.Context, src model.FileStreamer, size int64, info uploadInfoResp, up driver.UpdateProgress) error {
	bucket, objectKey, err := d.newOSSBucket(info)
	if err != nil {
		return err
	}
	err = bucket.PutObject(
		objectKey,
		driver.NewLimitedUploadStream(ctx, io.TeeReader(src, driver.NewProgress(size, up))),
	)
	if err != nil {
		return err
	}
	up(100)
	return nil
}

func (d *AliDoc) multipartUpload(ctx context.Context, src model.FileStreamer, size int64, info uploadInfoResp, up driver.UpdateProgress) error {
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
	ss, err := streamPkg.NewStreamSectionReader(src, int(partSize), &up)
	if err != nil {
		return err
	}

	var offset int64
	for partNumber := 1; partNumber <= partNum; partNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		length := partSize
		if remain := size - offset; remain < length {
			length = remain
		}

		reader, err := ss.GetSectionReader(offset, length)
		if err != nil {
			return err
		}
		var part oss.UploadPart
		var uploadErr error
		err = retry.Do(func() error {
			if _, err := reader.Seek(0, io.SeekStart); err != nil {
				return err
			}
			part, uploadErr = bucket.UploadPart(
				imur,
				driver.NewLimitedUploadStream(ctx, reader),
				length,
				partNumber,
			)
			return uploadErr
		},
			retry.Context(ctx),
			retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second),
		)
		ss.FreeSectionReader(reader)
		if err != nil {
			return err
		}
		parts = append(parts, part)
		up(100 * float64(len(parts)) / float64(partNum+1))
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
