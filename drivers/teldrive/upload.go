package teldrive

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// create empty file
func (d *Teldrive) touch(name, path string) error {
	uploadBody := base.Json{
		"name": name,
		"type": "file",
		"path": path,
	}
	if err := d.request(http.MethodPost, "/api/files", func(req *resty.Request) {
		req.SetBody(uploadBody)
	}, nil); err != nil {
		return err
	}

	return nil
}

func getMD5Hash(text string) string {
	hash := md5.Sum([]byte(text))
	return hex.EncodeToString(hash[:])
}

func (d *Teldrive) createFileOnUploadSuccess(name, id, path string, uploadedFileParts []FilePart, totalSize int64) error {
	remoteFileParts, err := d.getFilePart(id)
	if err != nil {
		return err
	}
	// check if the uploaded file parts match the remote file parts
	if len(remoteFileParts) != len(uploadedFileParts) {
		return fmt.Errorf("[Teldrive] file parts count mismatch: expected %d, got %d", len(uploadedFileParts), len(remoteFileParts))
	}
	formatParts := make([]base.Json, 0)
	for _, p := range remoteFileParts {
		formatParts = append(formatParts, base.Json{
			"id":   p.PartId,
			"salt": p.Salt,
		})
	}
	uploadBody := base.Json{
		"name":  name,
		"type":  "file",
		"path":  path,
		"parts": formatParts,
		"size":  totalSize,
	}
	// create file here
	if err := d.request(http.MethodPost, "/api/files", func(req *resty.Request) {
		req.SetBody(uploadBody)
	}, nil); err != nil {
		return err
	}

	return nil
}

func (d *Teldrive) checkFilePartExist(fileId string, partId int) (FilePart, error) {
	var uploadedParts []FilePart
	var filePart FilePart

	if err := d.request(http.MethodGet, "/api/uploads/{id}", func(req *resty.Request) {
		req.SetPathParam("id", fileId)
	}, &uploadedParts); err != nil {
		return filePart, err
	}

	for _, part := range uploadedParts {
		if part.PartId == partId {
			return part, nil
		}
	}

	return filePart, nil
}

func (d *Teldrive) getFilePart(fileId string) ([]FilePart, error) {
	var uploadedParts []FilePart
	if err := d.request(http.MethodGet, "/api/uploads/{id}", func(req *resty.Request) {
		req.SetPathParam("id", fileId)
	}, &uploadedParts); err != nil {
		return nil, err
	}

	return uploadedParts, nil
}

func (d *Teldrive) singleUploadRequest(ctx context.Context, fileId string, callback base.ReqCallback, resp any) error {
	url := d.Address + "/api/uploads/" + fileId
	client := resty.New().SetTimeout(0)

	req := client.R().
		SetContext(ctx)
	req.SetHeader("Cookie", d.Cookie)
	req.SetHeader("Content-Type", "application/octet-stream")
	req.SetContentLength(true)
	req.AddRetryCondition(func(r *resty.Response, err error) bool {
		return false
	})
	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}
	var e ErrResp
	req.SetError(&e)
	_req, err := req.Execute(http.MethodPost, url)
	if err != nil {
		return err
	}

	if _req.IsError() {
		return &e
	}
	return nil
}

func (d *Teldrive) doSingleUpload(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up model.UpdateProgress,
	maxRetried, totalParts int, chunkSize int64, fileId string) error {

	totalSize := file.GetSize()
	var fileParts []FilePart
	var uploaded int64 = 0
	var partName string
	chunkSize = min(totalSize, chunkSize)
	ss, err := stream.NewStreamSectionReader(file, int(chunkSize), &up)
	if err != nil {
		return err
	}
	chunkCnt := 0
	for uploaded < totalSize {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		curChunkSize := min(totalSize-uploaded, chunkSize)
		rd, err := ss.GetSectionReader(uploaded, curChunkSize)
		if err != nil {
			return err
		}
		chunkCnt += 1
		filePart := &FilePart{}
		if err := retry.Do(func() error {

			if _, err := rd.Seek(0, io.SeekStart); err != nil {
				return err
			}

			if d.RandomChunkName {
				partName = getMD5Hash(uuid.New().String())
			} else {
				partName = file.GetName()
				if totalParts > 1 {
					partName = fmt.Sprintf("%s.part.%03d", file.GetName(), chunkCnt)
				}
			}

			if err := d.singleUploadRequest(ctx, fileId, func(req *resty.Request) {
				uploadParams := map[string]string{
					"partName": partName,
					"partNo":   strconv.Itoa(chunkCnt),
					"fileName": file.GetName(),
				}
				req.SetQueryParams(uploadParams)
				req.SetBody(driver.NewLimitedUploadStream(ctx, rd))
				req.SetHeader("Content-Length", strconv.FormatInt(curChunkSize, 10))
			}, filePart); err != nil {
				return err
			}

			return nil
		},
			retry.Context(ctx),
			retry.Attempts(uint(maxRetried)),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second)); err != nil {
			return err
		}

		if filePart.Name != "" {
			fileParts = append(fileParts, *filePart)
			uploaded += curChunkSize
			up(float64(uploaded) / float64(totalSize) * 100)
			ss.FreeSectionReader(rd)
		} else {
			// For common situation this code won't reach
			return fmt.Errorf("[Teldrive] upload chunk %d failed: filePart Somehow missing", chunkCnt)
		}

	}

	return d.createFileOnUploadSuccess(file.GetName(), fileId, dstDir.GetPath(), fileParts, totalSize)
}

func (d *Teldrive) doMultiUpload(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up model.UpdateProgress,
	maxRetried, totalParts int, chunkSize int64, fileId string) error {

	concurrent := d.UploadConcurrency
	g, ctx := errgroup.WithContext(ctx)
	sem := semaphore.NewWeighted(int64(concurrent))
	chunkChan := make(chan chunkTask, concurrent*2)
	resultChan := make(chan FilePart, concurrent)
	totalSize := file.GetSize()

	ss, err := stream.NewStreamSectionReader(file, int(totalSize), &up)
	if err != nil {
		return err
	}
	ssLock := sync.Mutex{}
	g.Go(func() error {
		defer close(chunkChan)

		chunkIdx := 0
		for chunkIdx < totalParts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			offset := int64(chunkIdx) * chunkSize
			curChunkSize := min(totalSize-offset, chunkSize)

			ssLock.Lock()
			reader, err := ss.GetSectionReader(offset, curChunkSize)
			ssLock.Unlock()

			if err != nil {
				return err
			}
			task := chunkTask{
				chunkIdx:  chunkIdx + 1,
				chunkSize: curChunkSize,
				fileName:  file.GetName(),
				reader:    reader,
				ss:        ss,
			}
			// freeSectionReader will be called in d.uploadSingleChunk
			select {
			case chunkChan <- task:
				chunkIdx++
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	for i := 0; i < int(concurrent); i++ {
		g.Go(func() error {
			for task := range chunkChan {
				if err := sem.Acquire(ctx, 1); err != nil {
					return err
				}

				filePart, err := d.uploadSingleChunk(ctx, fileId, task, totalParts, maxRetried)
				sem.Release(1)

				if err != nil {
					return fmt.Errorf("upload chunk %d failed: %w", task.chunkIdx, err)
				}

				select {
				case resultChan <- *filePart:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}
	var fileParts []FilePart
	var collectErr error
	collectDone := make(chan struct{})

	go func() {
		defer close(collectDone)
		fileParts = make([]FilePart, 0, totalParts)

		done := make(chan error, 1)
		go func() {
			done <- g.Wait()
			close(resultChan)
		}()

		for {
			select {
			case filePart, ok := <-resultChan:
				if !ok {
					collectErr = <-done
					return
				}
				fileParts = append(fileParts, filePart)
			case err := <-done:
				collectErr = err
				return
			}
		}
	}()

	<-collectDone

	if collectErr != nil {
		return fmt.Errorf("multi-upload failed: %w", collectErr)
	}
	sort.Slice(fileParts, func(i, j int) bool {
		return fileParts[i].PartNo < fileParts[j].PartNo
	})

	return d.createFileOnUploadSuccess(file.GetName(), fileId, dstDir.GetPath(), fileParts, totalSize)
}

func (d *Teldrive) uploadSingleChunk(ctx context.Context, fileId string, task chunkTask, totalParts, maxRetried int) (*FilePart, error) {
	filePart := &FilePart{}
	retryCount := 0
	var partName string
	defer task.ss.FreeSectionReader(task.reader)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if existingPart, err := d.checkFilePartExist(fileId, task.chunkIdx); err == nil && existingPart.Name != "" {
			return &existingPart, nil
		}

		if _, err := task.reader.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}

		if d.RandomChunkName {
			partName = getMD5Hash(uuid.New().String())
		} else {
			partName = task.fileName
			if totalParts > 1 {
				partName = fmt.Sprintf("%s.part.%03d", task.fileName, task.chunkIdx)
			}
		}

		err := d.singleUploadRequest(ctx, fileId, func(req *resty.Request) {
			uploadParams := map[string]string{
				"partName": partName,
				"partNo":   strconv.Itoa(task.chunkIdx),
				"fileName": task.fileName,
			}
			req.SetQueryParams(uploadParams)
			req.SetBody(driver.NewLimitedUploadStream(ctx, task.reader))
			req.SetHeader("Content-Length", strconv.Itoa(int(task.chunkSize)))
		}, filePart)

		if err == nil {
			return filePart, nil
		}

		if retryCount >= maxRetried {
			return nil, fmt.Errorf("upload failed after %d retries: %w", maxRetried, err)
		}

		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			continue
		}

		retryCount++
		utils.Log.Errorf("[Teldrive] upload error: %v, retrying %d times", err, retryCount)

		backoffDuration := time.Duration(retryCount*retryCount) * time.Second
		if backoffDuration > 30*time.Second {
			backoffDuration = 30 * time.Second
		}

		select {
		case <-time.After(backoffDuration):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
