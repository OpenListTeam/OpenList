package _115_open

import (
	"context"
	"encoding/base64"
	"io"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/avast/retry-go"
)

func calPartSize(fileSize int64) int64 {
	var partSize int64 = 20 * utils.MB
	if fileSize > partSize {
		if fileSize > 1*utils.TB { // file Size over 1TB
			partSize = 5 * utils.GB // file part size 5GB
		} else if fileSize > 768*utils.GB { // over 768GB
			partSize = 109951163 // ≈ 104.8576MB, split 1TB into 10,000 part
		} else if fileSize > 512*utils.GB { // over 512GB
			partSize = 82463373 // ≈ 78.6432MB
		} else if fileSize > 384*utils.GB { // over 384GB
			partSize = 54975582 // ≈ 52.4288MB
		} else if fileSize > 256*utils.GB { // over 256GB
			partSize = 41231687 // ≈ 39.3216MB
		} else if fileSize > 128*utils.GB { // over 128GB
			partSize = 27487791 // ≈ 26.2144MB
		}
	}
	return partSize
}

func (d *Open115) singleUpload(ctx context.Context, tempF model.File, tokenResp *sdk.UploadGetTokenResp, initResp *sdk.UploadInitResp) error {
	ossClient, err := oss.New(tokenResp.Endpoint, tokenResp.AccessKeyId, tokenResp.AccessKeySecret, oss.SecurityToken(tokenResp.SecurityToken))
	if err != nil {
		return err
	}
	bucket, err := ossClient.Bucket(initResp.Bucket)
	if err != nil {
		return err
	}

	err = bucket.PutObject(initResp.Object, tempF,
		oss.Callback(base64.StdEncoding.EncodeToString([]byte(initResp.Callback.Value.Callback))),
		oss.CallbackVar(base64.StdEncoding.EncodeToString([]byte(initResp.Callback.Value.CallbackVar))),
	)

	return err
}

// type CallbackResult struct {
// 	State   bool   `json:"state"`
// 	Code    int    `json:"code"`
// 	Message string `json:"message"`
// 	Data    struct {
// 		PickCode string `json:"pick_code"`
// 		FileName string `json:"file_name"`
// 		FileSize int64  `json:"file_size"`
// 		FileID   string `json:"file_id"`
// 		ThumbURL string `json:"thumb_url"`
// 		Sha1     string `json:"sha1"`
// 		Aid      int    `json:"aid"`
// 		Cid      string `json:"cid"`
// 	} `json:"data"`
// }

func (d *Open115) multpartUpload(ctx context.Context, stream model.FileStreamer, up driver.UpdateProgress, tokenResp *sdk.UploadGetTokenResp, initResp *sdk.UploadInitResp) error {
	ossClient, err := oss.New(tokenResp.Endpoint, tokenResp.AccessKeyId, tokenResp.AccessKeySecret, oss.SecurityToken(tokenResp.SecurityToken))
	if err != nil {
		return err
	}
	bucket, err := ossClient.Bucket(initResp.Bucket)
	if err != nil {
		return err
	}

	imur, err := bucket.InitiateMultipartUpload(initResp.Object, oss.Sequential())
	if err != nil {
		return err
	}

	fileSize := stream.GetSize()
	chunkSize := calPartSize(fileSize)
	ss, err := streamPkg.NewStreamSectionReader(stream, int(chunkSize), &up)
	if err != nil {
		return err
	}

	partNum := (stream.GetSize() + chunkSize - 1) / chunkSize
	parts := make([]oss.UploadPart, partNum)
	offset := int64(0)
	for i := int64(1); i <= partNum; i++ {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}

		partSize := chunkSize
		if i == partNum {
			partSize = fileSize - (i-1)*chunkSize
		}
		rd, err := ss.GetSectionReader(offset, partSize)
		if err != nil {
			return err
		}
		err = retry.Do(func() error {
			rd.Seek(0, io.SeekStart)
			part, err := bucket.UploadPart(imur, driver.NewLimitedUploadStream(ctx, rd), partSize, int(i))
			if err != nil {
				return err
			}
			parts[i-1] = part
			return nil
		},
			retry.Context(ctx),
			retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second))
		ss.FreeSectionReader(rd)
		if err != nil {
			return err
		}

		if i == partNum {
			offset = fileSize
		} else {
			offset += partSize
		}
		up(float64(offset) * 100 / float64(fileSize))
	}

	// callbackRespBytes := make([]byte, 1024)
	_, err = bucket.CompleteMultipartUpload(
		imur,
		parts,
		oss.Callback(base64.StdEncoding.EncodeToString([]byte(initResp.Callback.Value.Callback))),
		oss.CallbackVar(base64.StdEncoding.EncodeToString([]byte(initResp.Callback.Value.CallbackVar))),
		// oss.CallbackResult(&callbackRespBytes),
	)
	if err != nil {
		return err
	}

	return nil
}
