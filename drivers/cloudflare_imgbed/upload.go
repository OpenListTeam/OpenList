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
	"path"
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

func (d *CFImgBed) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (newObj model.Obj, err error) {
	if file.GetSize() < hfDirectThreshold {
		newObj, err = d.standardUpload(ctx, dstDir, file, up)
	} else {
		switch d.LargeChannelType {
		case "huggingface":
			newObj, err = d.hfDirectUpload(ctx, dstDir, file, up)
		case "telegram", "cfr2", "s3", "discord":
			newObj, err = d.chunkedUpload(ctx, dstDir, file, up, d.LargeChannelType, d.LargeChannelName)
		default:
			newObj, err = d.standardUpload(ctx, dstDir, file, up)
		}
	}
	if newObj != nil && model.ObjHasMask(dstDir, model.Virtual) {
		key := dstDir.GetPath()
		for d.virtualDir.Delete(key) {
			key = path.Dir(key)
		}
	}
	return
}

// standardUpload 通过普通 multipart 表单上传。
// 使用 io.MultiReader 实现虚拟拼接，避免将整个大文件读入内存构建表单。
func (d *CFImgBed) standardUpload(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {

	channelName := d.SmallChannelName
	if file.GetSize() >= hfDirectThreshold {
		channelName = d.LargeChannelName
		log.WithField("size", file.GetSize()).Warn("large file falls back to standard upload, consider configuring LargeChannelType")
	}
	if channelName == "" {
		return nil, fmt.Errorf("channel name not configured")
	}

	// 1. 将参数放入 Query String
	reqUrl, err := url.Parse(d.Address + uploadApi)
	if err != nil {
		return nil, err
	}
	q := reqUrl.Query()
	q.Set("returnFormat", "default")
	q.Set("channelName", channelName)
	q.Set("uploadFolder", dstDir.GetPath())
	q.Set("autoRetry", "true")
	reqUrl.RawQuery = q.Encode()

	// 2. 构建 multipart 表单的头部
	b := bytes.NewBuffer(make([]byte, 0, 164+len(file.GetName()))) // 预估头部大小，避免频繁扩容
	w := multipart.NewWriter(b)
	_, err = w.CreateFormFile("file", file.GetName())
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

	if resp[0].PublicUrl != "" {
		if u, err := url.Parse(resp[0].PublicUrl); err == nil {
			d.publicUrlPrefix = u.Scheme + "://" + u.Host
		}
	}

	return &model.Object{
		Path:     srcPath,
		Name:     file.GetName(),
		Size:     file.GetSize(),
		Modified: file.ModTime(),
		IsFolder: false,
	}, nil
}

func (d *CFImgBed) chunkedUpload(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress, channelType, channelName string) (model.Obj, error) {
	if channelName == "" {
		return nil, fmt.Errorf("channel name not configured for chunked upload")
	}

	fileSize := file.GetSize()
	fileMime := file.GetMimetype()
	fileName := file.GetName()

	var chunkSizeMap = map[string]int64{

	}
	chunkSize := chunkSizeMap[channelType]
	if chunkSize == 0 {
		chunkSize = 5 * 1024 * 1024
	}
	totalChunks := int((fileSize + chunkSize - 1) / chunkSize)

	// 第一步：initChunked
	var initResp struct {
		Success  bool   `json:"success"`
		UploadId string `json:"uploadId"`
	}
	_, err := d.doRequest(ctx, http.MethodPost, uploadApi, func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"initChunked":   "true",
			"uploadChannel": channelType,
			"channelName":   channelName,
		})
		req.SetFormData(map[string]string{
			"originalFileName": fileName,
			"originalFileType": fileMime,
			"totalChunks":      strconv.Itoa(totalChunks),
		})
	}, &initResp)
	if err != nil {
		return nil, fmt.Errorf("initChunked failed: %w", err)
	}
	if !initResp.Success || initResp.UploadId == "" {
		return nil, fmt.Errorf("initChunked returned no uploadId")
	}
	uploadId := initResp.UploadId

	// 第二步：逐块上传
	ss, err := stream.NewStreamSectionReader(file, int(chunkSize), nil)
	if err != nil {
		return nil, err
	}

	reqUrl := d.Address + uploadApi + "?chunked=true&uploadChannel=" + channelType + "&channelName=" + channelName
	b := bytes.NewBuffer(make([]byte, 0, 2048))

	g, uploadCtx := errgroup.NewOrderedGroupWithContext(ctx, min(d.UploadThread, totalChunks),
		retry.Attempts(3),
		retry.Delay(time.Second),
		retry.DelayType(retry.BackOffDelay))

	for i := 0; i < totalChunks; i++ {
		if utils.IsCanceled(uploadCtx) {
			break
		}
		chunkIndex := i
		offset := int64(chunkIndex) * chunkSize
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

				b.Reset()
				w := multipart.NewWriter(b)
				_ = w.WriteField("uploadId", uploadId)
				_ = w.WriteField("chunkIndex", strconv.Itoa(chunkIndex))
				_ = w.WriteField("totalChunks", strconv.Itoa(totalChunks))
				_ = w.WriteField("originalFileName", fileName)
				_ = w.WriteField("originalFileType", fileMime)
				_, _ = w.CreateFormFile("file", fileName)
				headSize := b.Len()
				_ = w.Close()
				head := bytes.NewReader(b.Bytes()[:headSize])
				tail := bytes.NewReader(b.Bytes()[headSize:])

				rateLimitedRd := driver.NewLimitedUploadStream(ctx, io.MultiReader(head, reader, tail))
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqUrl, rateLimitedRd)
				if err != nil {
					return err
				}
				req.Header.Set("Content-Type", w.FormDataContentType())
				req.Header.Set("Authorization", "Bearer "+d.Token)
				req.ContentLength = int64(headSize) + sizeToRead + int64(b.Len()-headSize)

				res, err := base.HttpClient.Do(req)
				if err != nil {
					return err
				}
				defer res.Body.Close()
				if res.StatusCode != http.StatusOK {
					return fmt.Errorf("chunk %d upload failed: %d", chunkIndex, res.StatusCode)
				}

				up(90 * float64(chunkIndex+1) / float64(totalChunks))
				return nil
			},
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// 第三步：merge
	var mergeResp standardUploadResp
	_, err = d.doRequest(ctx, http.MethodPost, uploadApi, func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"chunked":       "true",
			"merge":         "true",
			"uploadChannel": channelType,
			"channelName":   channelName,
			"returnFormat":  "default",
			"uploadFolder":  dstDir.GetPath(),
		})
		req.SetFormData(map[string]string{
			"uploadId":         uploadId,
			"totalChunks":      strconv.Itoa(totalChunks),
			"originalFileName": fileName,
			"originalFileType": fileMime,
		})
	}, &mergeResp)
	if err != nil {
		return nil, fmt.Errorf("merge failed: %w", err)
	}
	if len(mergeResp) == 0 || mergeResp[0].Src == "" {
		return nil, fmt.Errorf("merge returned no src")
	}

	up(95)

	srcPath := strings.TrimPrefix(mergeResp[0].Src, "/file/")
	srcPath = strings.TrimPrefix(srcPath, "/")

	if mergeResp[0].PublicUrl != "" {
		if u, err := url.Parse(mergeResp[0].PublicUrl); err == nil {
			d.publicUrlPrefix = u.Scheme + "://" + u.Host
		}
	}

	return &model.Object{
		Path:     srcPath,
		Name:     fileName,
		Size:     fileSize,
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
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
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
	_, err = d.doRequest(ctx, http.MethodPost, hfGetUrlApi, func(req *resty.Request) {
		req.SetBody(reqBody)
		req.SetHeader("Content-Type", "application/json")
	}, &getUrlResp)
	if err != nil {
		return nil, err
	}

	// 秒传逻辑
	if getUrlResp.AlreadyExists || !getUrlResp.NeedsLfs {
		up(100)
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
			if k != "chunk_size" {
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
		mergeReq, err := http.NewRequestWithContext(ctx, http.MethodPost, href, bytes.NewReader(mergeBody))
		if err != nil {
			return nil, err
		}
		mergeReq.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		if d.Token != "" {
			mergeReq.Header.Set("Authorization", "Bearer "+d.Token)
		}

		res, err := base.HttpClient.Do(mergeReq)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body) // 读取 HF 返回的错误详情
		if res.StatusCode != http.StatusOK {
			log.WithField("status", res.StatusCode).WithField("response", string(body)).Error("HF merge chunks failed")
			return nil, fmt.Errorf("merge chunks failed: %s", string(body))
		}
		up(97)

	} else {
		// 单文件直传 (PUT)
		limitedReader := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
			Reader:         file,
			UpdateProgress: model.UpdateProgressWithRange(up, 0, 97),
		})

		req, err := http.NewRequestWithContext(ctx, http.MethodPut, href, limitedReader)
		if err != nil {
			return nil, err
		}
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
	_, err := d.doRequest(ctx, http.MethodPost, hfCommitApi, func(req *resty.Request) {
		req.SetBody(commitBody)
	}, &commitResp)
	if err != nil {
		return nil, fmt.Errorf("HF commit request failed: %w", err)
	}
	if !commitResp.Success {
		return nil, fmt.Errorf("HF commit failed: success=false")
	}

	srcPath := strings.TrimPrefix(commitResp.Src, "/file/")
	srcPath = strings.TrimPrefix(srcPath, "/")

	if commitResp.PublicUrl != "" {
		if u, err := url.Parse(commitResp.PublicUrl); err == nil {
			d.publicUrlPrefix = u.Scheme + "://" + u.Host
		}
	}

	return &model.Object{
		Path:     srcPath,
		Name:     fileName,
		Size:     fileSize,
		Modified: modTime,
		IsFolder: false,
	}, nil
}
