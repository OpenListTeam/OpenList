package _123_open

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/errgroup"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
	"github.com/go-resty/resty/v2"
)

// 创建文件 V2
func (d *Open123) create(parentFileID int64, filename string, etag string, size int64, duplicate int, containDir bool) (*UploadCreateResp, error) {
	var resp UploadCreateResp
	_, err := d.Request(UploadCreate, http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"parentFileId": parentFileID,
			"filename":     filename,
			"etag":         strings.ToLower(etag),
			"size":         size,
			"duplicate":    duplicate,
			"containDir":   containDir,
		})
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// 上传分片 V2
func (d *Open123) Upload(ctx context.Context, file model.FileStreamer, createResp *UploadCreateResp, up driver.UpdateProgress) error {
	uploadDomain := createResp.Data.Servers[0]
	size := file.GetSize()
	chunkSize := createResp.Data.SliceSize

	ss, err := stream.NewStreamSectionReader(file, int(chunkSize), &up)
	if err != nil {
		return err
	}

	uploadNums := (size + chunkSize - 1) / chunkSize
	thread := min(int(uploadNums), d.UploadThread)
	threadG, uploadCtx := errgroup.NewOrderedGroupWithContext(ctx, thread,
		retry.Attempts(3),
		retry.Delay(time.Second),
		retry.DelayType(retry.BackOffDelay))

	for partIndex := range uploadNums {
		if utils.IsCanceled(uploadCtx) {
			break
		}
		partIndex := partIndex
		partNumber := partIndex + 1 // 分片号从1开始
		offset := partIndex * chunkSize
		size := min(chunkSize, size-offset)
		var reader *stream.SectionReader
		var rateLimitedRd io.Reader
		sliceMD5 := ""
		// 表单
		b := bytes.NewBuffer(make([]byte, 0, 2048))
		threadG.GoWithLifecycle(errgroup.Lifecycle{
			Before: func(ctx context.Context) error {
				if reader == nil {
					var err error
					// 每个分片一个reader
					reader, err = ss.GetSectionReader(offset, size)
					if err != nil {
						return err
					}
					// 计算当前分片的MD5
					sliceMD5, err = utils.HashReader(utils.MD5, reader)
					if err != nil {
						return err
					}
				}
				return nil
			},
			Do: func(ctx context.Context) error {
				// 重置分片reader位置，因为HashReader、上一次失败已经读取到分片EOF
				reader.Seek(0, io.SeekStart)

				b.Reset()
				w := multipart.NewWriter(b)
				// 添加表单字段
				err = w.WriteField("preuploadID", createResp.Data.PreuploadID)
				if err != nil {
					return err
				}
				err = w.WriteField("sliceNo", strconv.FormatInt(partNumber, 10))
				if err != nil {
					return err
				}
				err = w.WriteField("sliceMD5", sliceMD5)
				if err != nil {
					return err
				}
				// 写入文件内容
				_, err = w.CreateFormFile("slice", fmt.Sprintf("%s.part%d", file.GetName(), partNumber))
				if err != nil {
					return err
				}
				headSize := b.Len()
				err = w.Close()
				if err != nil {
					return err
				}
				head := bytes.NewReader(b.Bytes()[:headSize])
				tail := bytes.NewReader(b.Bytes()[headSize:])
				rateLimitedRd = driver.NewLimitedUploadStream(ctx, io.MultiReader(head, reader, tail))
				// 创建请求并设置header
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadDomain+"/upload/v2/file/slice", rateLimitedRd)
				if err != nil {
					return err
				}

				// 设置请求头
				req.Header.Add("Authorization", "Bearer "+d.AccessToken)
				req.Header.Add("Content-Type", w.FormDataContentType())
				req.Header.Add("Platform", "open_platform")

				res, err := base.HttpClient.Do(req)
				if err != nil {
					return err
				}
				defer res.Body.Close()
				if res.StatusCode != 200 {
					return fmt.Errorf("slice %d upload failed, status code: %d", partNumber, res.StatusCode)
				}
				var resp BaseResp
				respBody, err := io.ReadAll(res.Body)
				if err != nil {
					return err
				}
				err = json.Unmarshal(respBody, &resp)
				if err != nil {
					return err
				}
				if resp.Code != 0 {
					return fmt.Errorf("slice %d upload failed: %s", partNumber, resp.Message)
				}

				progress := 10.0 + 85.0*float64(threadG.Success())/float64(uploadNums)
				up(progress)
				return nil
			},
			After: func(err error) {
				ss.FreeSectionReader(reader)
			},
		})
	}

	if err := threadG.Wait(); err != nil {
		return err
	}

	return nil
}

// 上传完毕
func (d *Open123) complete(preuploadID string) (*UploadCompleteResp, error) {
	var resp UploadCompleteResp
	_, err := d.Request(UploadComplete, http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"preuploadID": preuploadID,
		})
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}
