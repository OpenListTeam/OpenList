package cloudflare_imgbed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/errgroup"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

func (d *CFImgBed) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	fileSize := file.GetSize()
	// 如果文件较大且配置了 HuggingFace 渠道，走直传流程
	if fileSize >= hfDirectThreshold && d.LargeChannelType == "huggingface" {
		log.WithField("size", fileSize).Info("file exceeds threshold, using HuggingFace direct upload")
		return d.hfDirectUpload(ctx, dstDir, file, up)
	}
	// 否则走普通图床 API 上传
	return d.standardUpload(ctx, dstDir, file, up)
}

// standardUpload 通过普通 multipart 表单上传。
// 使用 io.MultiReader 实现虚拟拼接，避免将整个大文件读入内存构建表单。
func (d *CFImgBed) standardUpload(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	fileName := file.GetName()
	fileSize := file.GetSize()
	fileMime := file.GetMimetype()
	uploadDir := dstDir.GetPath()

	channelName := d.SmallChannelName
	if fileSize >= hfDirectThreshold {
		channelName = d.LargeChannelName
		log.WithField("size", fileSize).Warn("File exceeds threshold but non-HF channel is used.")
	}
	if channelName == "" {
		return nil, fmt.Errorf("channel name not configured")
	}

	// 1. 将参数放入 Query String
	reqUrl, _ := url.Parse(strings.TrimRight(d.Address, "/") + UploadApi)
	q := reqUrl.Query()
	if uploadDir != "" {
		q.Set("uploadFolder", uploadDir)
	}
	q.Set("returnFormat", "default")
	q.Set("channelName", channelName)
	reqUrl.RawQuery = q.Encode()

	// 2. 构建 multipart 表单的头部
	var headBuf bytes.Buffer
	w := multipart.NewWriter(&headBuf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeQuotes(fileName)))
	if fileMime == "" {
		fileMime = "application/octet-stream"
	}
	h.Set("Content-Type", fileMime)
	if _, err := w.CreatePart(h); err != nil {
		return nil, err
	}
	boundary := w.Boundary()
	tailStr := fmt.Sprintf("\r\n--%s--\r\n", boundary)

	reader, err := getFileReader(file)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	progressReader := &progressReadCloser{ReadCloser: reader, total: fileSize, up: up}

	// 3. 将 [表单头 + 文件流 + 表单尾] 组合成单一 Reader
	bodyStream := io.MultiReader(
		bytes.NewReader(headBuf.Bytes()),
		progressReader,
		strings.NewReader(tailStr),
	)

	rateLimitedReader := driver.NewLimitedUploadStream(ctx, bodyStream)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqUrl.String(), rateLimitedReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.ContentLength = int64(headBuf.Len()) + fileSize + int64(len(tailStr))

	res, err := base.HttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upload failed %d: %s", res.StatusCode, string(body))
	}

	var resp standardUploadResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if len(resp) == 0 || resp[0].Src == "" {
		return nil, fmt.Errorf("no src returned")
	}

	srcPath := strings.TrimPrefix(resp[0].Src, "/file/")
	srcPath = strings.TrimPrefix(srcPath, "/")

	return &model.Object{
		ID:       srcPath,
		Path:     srcPath,
		Name:     fileName,
		Size:     fileSize,
		Modified: file.ModTime(),
		IsFolder: false,
	}, nil
}

// hfDirectUpload 处理 HuggingFace 的 LFS 直传逻辑（申请授权 -> 物理上传 -> 后端 Commit）
func (d *CFImgBed) hfDirectUpload(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	fileName := file.GetName()
	fileSize := file.GetSize()
	fileMime := file.GetMimetype()
	modTime := file.ModTime()
	uploadDir := dstDir.GetPath()

	sha256Hash, fileSample, err := prepareHFUploadData(file)
	if err != nil {
		return nil, err
	}

	channelName := d.LargeChannelName
	if channelName == "" {
		return nil, fmt.Errorf("LargeChannelName not configured")
	}

	// 1. 请求图床后端获取 HF 授权地址
	reqBody := map[string]interface{}{
		"fileName":     fileName,
		"fileType":     fileMime,
		"fileSize":     fileSize,
		"sha256":       sha256Hash,
		"fileSample":   fileSample,
		"channelName":  channelName,
		"uploadFolder": uploadDir,
	}

	var getUrlResp hfGetUrlResp
	_, err = d.doRequest(http.MethodPost, HFGetUrlApi, func(req *resty.Request) {
		req.SetBody(reqBody)
		req.SetHeader("Content-Type", "application/json")
	}, &getUrlResp)
	if err != nil {
		return nil, err
	}

	// 秒传逻辑
	if getUrlResp.AlreadyExists || !getUrlResp.NeedsLfs {
		return d.hfCommit(ctx, getUrlResp, fileName, fileSize, fileMime, modTime)
	}

	if getUrlResp.UploadAction == nil {
		return nil, fmt.Errorf("HF upload action is nil")
	}

	headers := getUrlResp.UploadAction.Header
	href := getUrlResp.UploadAction.Href

	if _, err := file.GetFile().Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

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

		ss, err := stream.NewStreamSectionReader(file, int(chunkSize), nil)
		if err != nil {
			return nil, err
		}

		g, uploadCtx := errgroup.NewOrderedGroupWithContext(ctx, d.UploadThread,
			retry.Attempts(3),
			retry.Delay(time.Second),
			retry.DelayType(retry.BackOffDelay))

		var partsMutex sync.Mutex
		parts := make([]map[string]interface{}, 0, totalParts)

		for partNumber := 1; partNumber <= totalParts; partNumber++ {
			partNumber := partNumber
			partUrl := partUrls[partNumber]
			offset := int64(partNumber-1) * chunkSize
			sizeToRead := chunkSize
			if offset+sizeToRead > fileSize {
				sizeToRead = fileSize - offset
			}

			g.GoWithLifecycle(errgroup.Lifecycle{
				Do: func(ctx context.Context) error {
					reader, err := ss.GetSectionReader(offset, sizeToRead)
					if err != nil {
						return err
					}
					defer ss.FreeSectionReader(reader)

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
					partsMutex.Lock()
					parts = append(parts, map[string]interface{}{"partNumber": partNumber, "etag": etag})
					partsMutex.Unlock()

					if up != nil {
						up(100 * float64(g.Success()+1) / float64(totalParts))
					}
					return nil
				},
			})
			if utils.IsCanceled(uploadCtx) {
				break
			}
		}

		if err := g.Wait(); err != nil {
			return nil, err
		}

		// 合并分片
		sort.Slice(parts, func(i, j int) bool { return parts[i]["partNumber"].(int) < parts[j]["partNumber"].(int) })
		mergeBody, _ := json.Marshal(map[string]interface{}{"oid": getUrlResp.Oid, "parts": parts})
		mergeReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, href, bytes.NewReader(mergeBody))
		mergeReq.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		for k, v := range headers {
			if k != "chunk_size" && len(k) != 5 {
				mergeReq.Header.Set(k, v)
			}
		}
		res, err := base.HttpClient.Do(mergeReq)
		if err != nil || res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("merge chunks failed")
		}
		res.Body.Close()

	} else {
		// 单文件直传 (PUT)
		cachedFile := file.GetFile()
		cachedFile.Seek(0, io.SeekStart)
		progressReader := &progressReadCloser{ReadCloser: io.NopCloser(cachedFile), total: fileSize, up: up}

		limitedReader := driver.NewLimitedUploadStream(ctx, progressReader)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, href, limitedReader)
		req.ContentLength = fileSize
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := base.HttpClient.Do(req)
		if err != nil || res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("direct upload failed")
		}
		res.Body.Close()
	}

	// 3. 通知图床后端完成文件登记
	return d.hfCommit(ctx, getUrlResp, fileName, fileSize, fileMime, modTime)
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
	_, err := d.doRequest(http.MethodPost, HFCommitApi, func(req *resty.Request) {
		req.SetBody(commitBody)
	}, &commitResp)
	if err != nil || !commitResp.Success {
		return nil, fmt.Errorf("HF commit failed")
	}

	srcPath := strings.TrimPrefix(commitResp.Src, "/file/")
	srcPath = strings.TrimPrefix(srcPath, "/")

	return &model.Object{
		ID:       srcPath,
		Path:     srcPath,
		Name:     fileName,
		Size:     fileSize,
		Modified: modTime,
		IsFolder: false,
	}, nil
}

func getFileReader(file model.FileStreamer) (io.ReadCloser, error) {
	if cached := file.GetFile(); cached != nil {
		if _, err := cached.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		if rc, ok := cached.(io.ReadCloser); ok {
			return rc, nil
		}
		return io.NopCloser(cached), nil
	}
	return io.NopCloser(file), nil
}

type progressReadCloser struct {
	io.ReadCloser
	total int64
	read  int64
	up    driver.UpdateProgress
}

func (r *progressReadCloser) Read(p []byte) (n int, err error) {
	n, err = r.ReadCloser.Read(p)
	r.read += int64(n)
	if r.total > 0 && r.up != nil {
		r.up(100 * float64(r.read) / float64(r.total))
	}
	return
}
