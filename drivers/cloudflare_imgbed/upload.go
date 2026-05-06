package cloudflare_imgbed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/errgroup"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

func (d *CFImgBed) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	fileSize := file.GetSize()
	// 如果文件较大且配置了 HuggingFace 渠道，走直传流程
	if fileSize >= hfDirectThreshold && d.LargeChannelType == "huggingface" {
		log.WithField("size", fileSize).Debug("file exceeds threshold, using HuggingFace direct upload")
		return d.hfDirectUpload(ctx, dstDir, file, up)
	}
	// 否则走普通图床 API 上传
	return d.standardUpload(ctx, dstDir, file, up)
}

// standardUpload 通过普通 multipart 表单上传。
// 使用 io.MultiReader 实现虚拟拼接，避免将整个大文件读入内存构建表单。
func (d *CFImgBed) standardUpload(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {

	channelName := d.SmallChannelName
	if file.GetSize() >= hfDirectThreshold {
		channelName = d.LargeChannelName
		log.WithField("size", file.GetSize()).Warn("File exceeds threshold but non-HF channel is used.")
	}
	if channelName == "" {
		return nil, fmt.Errorf("channel name not configured")
	}

	// 1. 将参数放入 Query String
	reqUrl, _ := url.Parse(d.Address + uploadApi)
	q := reqUrl.Query()
	q.Set("uploadFolder", dstDir.GetPath())
	q.Set("returnFormat", "default")
	q.Set("channelName", channelName)
	reqUrl.RawQuery = q.Encode()

	// 2. 构建 multipart 表单的头部
	b := bytes.NewBuffer(make([]byte, 0, 164+len(file.GetName()))) // 预估头部大小，避免频繁扩容
	w := multipart.NewWriter(b)
	_, err := w.CreateFormFile("file", file.GetName())
	if err != nil {
		return nil, err
	}
	headSize := b.Len()
	err = w.Close()
	if err != nil {
		return nil, err
	}
	head := bytes.NewReader(b.Bytes()[:headSize])
	tail := bytes.NewReader(b.Bytes()[headSize:])

	// 3. 将 [表单头 + 文件流 + 表单尾] 组合成单一 Reader
	rateLimitedReader := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
		Reader: &driver.SimpleReaderWithSize{
			Reader: io.MultiReader(head, file, tail),
			Size:   int64(b.Len()) + file.GetSize(),
		},
		UpdateProgress: up,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqUrl.String(), rateLimitedReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.ContentLength = int64(b.Len()) + file.GetSize()
	res, err := base.HttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	b.Reset()
	_, err = b.ReadFrom(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upload failed %d: %s", res.StatusCode, b.String())
	}

	var resp standardUploadResp
	if err := json.Unmarshal(b.Bytes(), &resp); err != nil {
		return nil, err
	}
	if len(resp) == 0 || resp[0].Src == "" {
		return nil, fmt.Errorf("no src returned")
	}

	srcPath := strings.TrimPrefix(resp[0].Src, "/file/")
	srcPath = strings.TrimPrefix(srcPath, "/")

	return &model.Object{
		Path:     srcPath,
		Name:     file.GetName(),
		Size:     file.GetSize(),
		Modified: file.ModTime(),
		IsFolder: false,
	}, nil
}

// hfDirectUpload 处理 HuggingFace 的 LFS 直传逻辑（申请授权 -> 物理上传 -> 后端 Commit）
func (d *CFImgBed) hfDirectUpload(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	channelName := d.LargeChannelName
	if channelName == "" {
		return nil, errors.New("LargeChannelName not configured")
	}

	sha256Hash := file.GetHash().GetHash(utils.SHA256)
	if len(sha256Hash) != utils.SHA256.Width {
		var err error
		_, sha256Hash, err = stream.CacheFullAndHash(file, &up, utils.SHA256)
		if err != nil {
			return nil, err
		}
	}

	fileSize := file.GetSize()
	sampleSize := min(fileSize, fileSampleSize)
	sampleRd, err := file.RangeRead(http_range.Range{Start: 0, Length: sampleSize})
	if err != nil {
		return nil, err
	}
	sampleBuf := make([]byte, sampleSize)
	_, err = io.ReadFull(sampleRd, sampleBuf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	fileSample := base64.StdEncoding.EncodeToString(sampleBuf)

	fileMime := file.GetMimetype()
	// 1. 请求图床后端获取 HF 授权地址
	reqBody := map[string]interface{}{
		"fileName":     file.GetName(),
		"fileType":     fileMime,
		"fileSize":     fileSize,
		"sha256":       sha256Hash,
		"fileSample":   fileSample,
		"channelName":  channelName,
		"uploadFolder": dstDir.GetPath(),
	}

	var getUrlResp hfGetUrlResp
	_, err = d.doRequest(http.MethodPost, hfGetUrlApi, func(req *resty.Request) {
		req.SetBody(reqBody)
		req.SetHeader("Content-Type", "application/json")
	}, &getUrlResp)
	if err != nil {
		return nil, err
	}

	// 秒传逻辑
	if getUrlResp.AlreadyExists || !getUrlResp.NeedsLfs {
		return d.hfCommit(ctx, getUrlResp, file.GetName(), fileSize, fileMime, file.ModTime())
	}

	if getUrlResp.UploadAction == nil {
		return nil, fmt.Errorf("HF upload action is nil")
	}

	headers := getUrlResp.UploadAction.Header
	href := getUrlResp.UploadAction.Href

	// 2. 根据响应判断是执行分片上传还是单文件上传
	chunkSizeStr, needChunk := headers["chunk_size"]
	if needChunk {
		// 分片直传 (AWS S3 Multipart 风格)
		chunkSize, _ := strconv.ParseInt(chunkSizeStr, 10, 64)
		if chunkSize <= 0 {
			chunkSize = 20 * 1024 * 1024
		}

		partUrls := make(map[int]string)
		for k, v := range headers {
			if len(k) == 5 { // 格式通常为 "00001", "00002"
				if idx, err := strconv.Atoi(k); err == nil {
					partUrls[idx] = v
				}
			}
		}
		totalParts := len(partUrls)

		ss, err := stream.NewStreamSectionReader(file, int(chunkSize), &up)
		if err != nil {
			return nil, err
		}

		g, uploadCtx := errgroup.NewOrderedGroupWithContext(ctx, min(d.UploadThread, totalParts),
			retry.Attempts(3),
			retry.Delay(time.Second),
			retry.DelayType(retry.BackOffDelay))

		parts := make([]map[string]any, totalParts)

		for partNumber := range partUrls {
			if utils.IsCanceled(uploadCtx) {
				break
			}
			partUrl := partUrls[partNumber]
			offset := int64(partNumber-1) * chunkSize
			sizeToRead := chunkSize
			if offset+sizeToRead > fileSize {
				sizeToRead = fileSize - offset
			}

			var reader io.ReadSeeker
			g.GoWithLifecycle(errgroup.Lifecycle{
				Before: func(ctx context.Context) (err error) {
					reader, err = ss.GetSectionReader(offset, sizeToRead)
					return
				},
				After: func(err error) {
					ss.FreeSectionReader(reader)
				},
				Do: func(ctx context.Context) (err error) {
					_, err = reader.Seek(0, io.SeekStart)
					if err != nil {
						return err
					}
					limitedReader := driver.NewLimitedUploadStream(ctx, reader)
					req, err := http.NewRequestWithContext(ctx, http.MethodPut, partUrl, limitedReader)
					if err != nil {
						return err
					}
					for key, val := range headers {
						if len(key) != 5 && key != "chunk_size" {
							req.Header.Set(key, val)
						}
					}
					req.ContentLength = sizeToRead

					res, err := base.HttpClient.Do(req)
					if err != nil {
						return err
					}
					defer res.Body.Close()

					if res.StatusCode != http.StatusOK {
						return fmt.Errorf("chunk %d failed: %d", partNumber, res.StatusCode)
					}

					etag := res.Header.Get("ETag")
					parts[partNumber-1] = map[string]any{"partNumber": partNumber, "etag": etag}

					up(95 * float64(g.Success()+1) / float64(totalParts))
					return nil
				},
			})
		}

		if err := g.Wait(); err != nil {
			return nil, err
		}

		// 合并分片
		// sort.Slice(parts, func(i, j int) bool { return parts[i]["partNumber"].(int) < parts[j]["partNumber"].(int) })
		mergeBody, _ := json.Marshal(map[string]any{"oid": getUrlResp.Oid, "parts": parts})
		mergeReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, href, bytes.NewReader(mergeBody))
		mergeReq.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		for k, v := range headers {
			if k != "chunk_size" && len(k) != 5 {
				mergeReq.Header.Set(k, v)
			}
		}
		res, err := base.HttpClient.Do(mergeReq)
		if err != nil {
			return nil, err
		}
		up(97)
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("merge chunks failed")
		}

	} else {
		// 单文件直传 (PUT)
		limitedReader := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
			Reader:         file,
			UpdateProgress: model.UpdateProgressWithRange(up, 0, 97),
		})

		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, href, limitedReader)
		req.ContentLength = fileSize
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := base.HttpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("direct upload failed")
		}
	}

	defer up(100)

	// 3. 通知图床后端完成文件登记
	return d.hfCommit(ctx, getUrlResp, file.GetName(), fileSize, fileMime, file.ModTime())
}

func (d *CFImgBed) hfCommit(ctx context.Context, getUrlResp hfGetUrlResp, fileName string, fileSize int64, fileMime string, modTime time.Time) (model.Obj, error) {
	commitBody := map[string]interface{}{
		"fullId":      getUrlResp.FullID,
		"filePath":    getUrlResp.FilePath,
		"sha256":      getUrlResp.Oid,
		"fileSize":    fileSize,
		"fileName":    fileName,
		"fileType":    fileMime,
		"channelName": getUrlResp.ChannelName,
	}
	var commitResp hfCommitResp
	_, err := d.doRequest(http.MethodPost, hfCommitApi, func(req *resty.Request) {
		req.SetBody(commitBody)
	}, &commitResp)
	if err != nil || !commitResp.Success {
		return nil, fmt.Errorf("HF commit failed")
	}

	srcPath := strings.TrimPrefix(commitResp.Src, "/file/")
	srcPath = strings.TrimPrefix(srcPath, "/")

	return &model.Object{
		Path:     srcPath,
		Name:     fileName,
		Size:     fileSize,
		Modified: modTime,
		IsFolder: false,
	}, nil
}
