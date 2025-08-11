package _123

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/errgroup"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
	"github.com/go-resty/resty/v2"
)

func (d *Pan123) getS3PreSignedUrls(ctx context.Context, upReq *UploadResp, start, end int) (*S3PreSignedURLs, error) {
	data := base.Json{
		"bucket":          upReq.Data.Bucket,
		"key":             upReq.Data.Key,
		"partNumberEnd":   end,
		"partNumberStart": start,
		"uploadId":        upReq.Data.UploadId,
		"StorageNode":     upReq.Data.StorageNode,
	}
	var s3PreSignedUrls S3PreSignedURLs
	_, err := d.Request(S3PreSignedUrls, http.MethodPost, func(req *resty.Request) {
		req.SetBody(data).SetContext(ctx)
	}, &s3PreSignedUrls)
	if err != nil {
		return nil, err
	}
	return &s3PreSignedUrls, nil
}

func (d *Pan123) getS3Auth(ctx context.Context, upReq *UploadResp, start, end int) (*S3PreSignedURLs, error) {
	data := base.Json{
		"StorageNode":     upReq.Data.StorageNode,
		"bucket":          upReq.Data.Bucket,
		"key":             upReq.Data.Key,
		"partNumberEnd":   end,
		"partNumberStart": start,
		"uploadId":        upReq.Data.UploadId,
	}
	var s3PreSignedUrls S3PreSignedURLs
	_, err := d.Request(S3Auth, http.MethodPost, func(req *resty.Request) {
		req.SetBody(data).SetContext(ctx)
	}, &s3PreSignedUrls)
	if err != nil {
		return nil, err
	}
	return &s3PreSignedUrls, nil
}

func (d *Pan123) completeS3(ctx context.Context, upReq *UploadResp, file model.FileStreamer, isMultipart bool) error {
	data := base.Json{
		"StorageNode": upReq.Data.StorageNode,
		"bucket":      upReq.Data.Bucket,
		"fileId":      upReq.Data.FileId,
		"fileSize":    file.GetSize(),
		"isMultipart": isMultipart,
		"key":         upReq.Data.Key,
		"uploadId":    upReq.Data.UploadId,
	}
	_, err := d.Request(UploadCompleteV2, http.MethodPost, func(req *resty.Request) {
		req.SetBody(data).SetContext(ctx)
	}, nil)
	return err
}

func (d *Pan123) newUpload(ctx context.Context, upReq *UploadResp, file model.FileStreamer, up driver.UpdateProgress) error {
	// fetch s3 pre signed urls
	size := file.GetSize()
	chunkSize := int64(16 * utils.MB)
	chunkCount := 1
	if size > chunkSize {
		chunkCount = int((size + chunkSize - 1) / chunkSize)
	}

	ss, err := stream.NewStreamSectionReader(file, int(chunkSize), &up)
	if err != nil {
		return err
	}

	lastChunkSize := size % chunkSize
	if lastChunkSize == 0 {
		lastChunkSize = chunkSize
	}
	// only 1 batch is allowed
	batchSize := 1
	getS3UploadUrl := d.getS3Auth
	if chunkCount > 1 {
		batchSize = 10
		getS3UploadUrl = d.getS3PreSignedUrls
	}

	thread := min(int(chunkCount), d.UploadThread)
	threadG, uploadCtx := errgroup.NewOrderedGroupWithContext(ctx, thread,
		retry.Attempts(3),
		retry.Delay(time.Second),
		retry.DelayType(retry.BackOffDelay))
	for i := 1; i <= chunkCount; i += batchSize {
		if utils.IsCanceled(uploadCtx) {
			break
		}
		start := i
		end := min(i+batchSize, chunkCount+1)
		s3PreSignedUrls, err := getS3UploadUrl(uploadCtx, upReq, start, end)
		if err != nil {
			return err
		}
		// upload each chunk
		for cur := start; cur < end; cur++ {
			if utils.IsCanceled(uploadCtx) {
				break
			}
			offset := int64(cur-1) * chunkSize
			curSize := chunkSize
			if cur == chunkCount {
				curSize = lastChunkSize
			}
			var reader *stream.SectionReader
			var rateLimitedRd io.Reader
			threadG.GoWithLifecycle(errgroup.Lifecycle{
				Before: func(ctx context.Context) error {
					if reader == nil {
						var err error
						reader, err = ss.GetSectionReader(offset, curSize)
						if err != nil {
							return err
						}
						rateLimitedRd = driver.NewLimitedUploadStream(ctx, reader)
					}
					return nil
				},
				Do: func(ctx context.Context) error {
					reader.Seek(0, io.SeekStart)
					uploadUrl := s3PreSignedUrls.Data.PreSignedUrls[strconv.Itoa(cur)]
					if uploadUrl == "" {
						return fmt.Errorf("upload url is empty, s3PreSignedUrls: %+v", s3PreSignedUrls)
					}
					reader.Seek(0, io.SeekStart)
					req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadUrl, rateLimitedRd)
					if err != nil {
						return err
					}
					req.ContentLength = curSize
					//req.Header.Set("Content-Length", strconv.FormatInt(curSize, 10))
					res, err := base.HttpClient.Do(req)
					if err != nil {
						return err
					}
					defer res.Body.Close()
					if res.StatusCode == http.StatusForbidden {
						singleflight.AnyGroup.Do(fmt.Sprintf("Pan123.newUpload_%p", threadG), func() (any, error) {
							newS3PreSignedUrls, err := getS3UploadUrl(ctx, upReq, cur, end)
							if err != nil {
								return nil, err
							}
							s3PreSignedUrls.Data.PreSignedUrls = newS3PreSignedUrls.Data.PreSignedUrls
							return nil, nil
						})
						if err != nil {
							return err
						}
						return fmt.Errorf("upload s3 chunk %d failed, status code: %d", cur, res.StatusCode)
					}
					if res.StatusCode != http.StatusOK {
						body, err := io.ReadAll(res.Body)
						if err != nil {
							return err
						}
						return fmt.Errorf("upload s3 chunk %d failed, status code: %d, body: %s", cur, res.StatusCode, body)
					}
					progress := 10.0 + 85.0*float64(threadG.Success())/float64(chunkCount)
					up(progress)
					return nil
				},
				After: func(err error) {
					ss.FreeSectionReader(reader)
				},
			})
		}
	}
	if err := threadG.Wait(); err != nil {
		return err
	}
	defer up(100)
	// complete s3 upload
	return d.completeS3(ctx, upReq, file, chunkCount > 1)
}
