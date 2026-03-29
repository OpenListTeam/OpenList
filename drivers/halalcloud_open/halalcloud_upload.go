package halalcloudopen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	sdkUserFile "github.com/halalcloud/golang-sdk-lite/halalcloud/services/userfile"
	"github.com/ipfs/go-cid"
	log "github.com/sirupsen/logrus"
)

func (d *HalalCloudOpen) put(ctx context.Context, dstDir model.Obj, fileStream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {

	newPath := path.Join(dstDir.GetPath(), fileStream.GetName())

	uploadTask, err := d.sdkUserFileService.CreateUploadTask(ctx, &sdkUserFile.File{
		Path: newPath,
		Size: fileStream.GetSize(),
	})
	if err != nil {
		return nil, err
	}

	if uploadTask.Created {
		return nil, nil
	}

	slicesList := make([]string, 0)
	codec := uint64(0x55)
	if uploadTask.BlockCodec > 0 {
		codec = uint64(uploadTask.BlockCodec)
	}
	blockHashType := uploadTask.BlockHashType
	mhType := uint64(0x12)
	if blockHashType > 0 {
		mhType = uint64(blockHashType)
	}
	prefix := cid.Prefix{
		Codec:    codec,
		MhLength: -1,
		MhType:   mhType,
		Version:  1,
	}
	blockSize := uploadTask.BlockSize
	//
	// Not sure whether FileStream supports concurrent read and write operations, so currently using single-threaded upload to ensure safety.
	// read file
	bufferSize := int(blockSize)
	buffer := make([]byte, bufferSize)
	offset := 0
	teeReader := io.TeeReader(fileStream, driver.NewProgress(fileStream.GetSize(), up))
	for {
		n, err := teeReader.Read(buffer[offset:]) // 这里 len(buf[offset:]) <= 4MB
		if n > 0 {
			offset += n
			if offset == int(blockSize) {
				uploadCid, err := postFileSlice(ctx, buffer, uploadTask.Task, uploadTask.UploadAddress, prefix, retryTimes)
				if err != nil {
					return nil, err
				}
				slicesList = append(slicesList, uploadCid.String())
				offset = 0
			}
		}

		if err != nil {
			if err == io.EOF {
				if offset > 0 {
					uploadCid, err := postFileSlice(ctx, buffer[:offset], uploadTask.Task, uploadTask.UploadAddress, prefix, retryTimes)
					if err != nil {
						return nil, err
					}
					slicesList = append(slicesList, uploadCid.String())
				}
				break
			}
			return nil, err
		}
	}
	newFile, err := makeFile(ctx, slicesList, uploadTask.Task, uploadTask.UploadAddress, retryTimes)
	if err != nil {
		return nil, err
	}

	return NewObjFile(newFile), nil

}

func makeFile(ctx context.Context, fileSlice []string, taskID string, uploadAddress string, retry int) (*sdkUserFile.File, error) {
	var lastError error = nil
	for range retry {
		newFile, err := doMakeFile(fileSlice, taskID, uploadAddress)
		if err == nil {
			return newFile, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		log.Errorf("make file slice failed, retrying... error: %s", err.Error())
		if strings.Contains(err.Error(), "not found") {
			return nil, err
		}
		lastError = err
		time.Sleep(slicePostErrorRetryInterval)
	}
	return nil, fmt.Errorf("mk file slice failed after %d times, error: %s", retry, lastError.Error())
}

func doMakeFile(fileSlice []string, taskID string, uploadAddress string) (*sdkUserFile.File, error) {
	accessUrl := uploadAddress + "/" + taskID
	getTimeOut := time.Minute * 2
	u, err := url.Parse(accessUrl)
	if err != nil {
		return nil, err
	}
	n, _ := json.Marshal(fileSlice)
	httpRequest := http.Request{
		Method: http.MethodPost,
		URL:    u,
		Header: map[string][]string{
			"Accept":       {"application/json"},
			"Content-Type": {"application/json"},
			//"Content-Length": {strconv.Itoa(len(n))},
		},
		Body: io.NopCloser(bytes.NewReader(n)),
	}
	httpClient := http.Client{
		Timeout: getTimeOut,
	}
	httpResponse, err := httpClient.Do(&httpRequest)
	if err != nil {
		return nil, err
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK && httpResponse.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(httpResponse.Body)
		message := string(b)
		log.Errorf("make file failed, status code: %d, message: %s", httpResponse.StatusCode, message)

		return nil, fmt.Errorf("mk file slice failed, status code: %d, message: %s", httpResponse.StatusCode, message)
	}
	b, _ := io.ReadAll(httpResponse.Body)
	var result *UploadedFile
	err = json.Unmarshal(b, &result)
	if err != nil {
		log.Errorf("make file failed from response, status code: %d, message: %s", httpResponse.StatusCode, string(b))
		return nil, err
	}
	return &sdkUserFile.File{
		Identity:        result.Identity,
		Path:            result.Path,
		Size:            result.Size,
		ContentIdentity: result.ContentIdentity,
	}, nil
}
func postFileSlice(ctx context.Context, fileSlice []byte, taskID string, uploadAddress string, preix cid.Prefix, retry int) (cid.Cid, error) {
	var lastError error = nil
	for range retry {
		newCid, err := doPostFileSlice(fileSlice, taskID, uploadAddress, preix)
		if err == nil {
			return newCid, nil
		}
		if ctx.Err() != nil {
			return cid.Undef, err
		}
		time.Sleep(slicePostErrorRetryInterval)
		lastError = err
	}
	return cid.Undef, fmt.Errorf("upload file slice failed after %d times, error: %s", retry, lastError.Error())
}
func doPostFileSlice(fileSlice []byte, taskID string, uploadAddress string, preix cid.Prefix) (cid.Cid, error) {
	// 1. sum file slice
	newCid, err := preix.Sum(fileSlice)
	if err != nil {
		return cid.Undef, err
	}
	// 2. post file slice
	sliceCidString := newCid.String()
	// /{taskID}/{sliceID}
	accessUrl := uploadAddress + "/" + taskID + "/" + sliceCidString
	getTimeOut := time.Second * 30
	// get {accessUrl} in {getTimeOut}
	u, err := url.Parse(accessUrl)
	if err != nil {
		return cid.Undef, err
	}
	// header: accept: application/json
	// header: content-type: application/octet-stream
	// header: content-length: {fileSlice.length}
	// header: x-content-cid: {sliceCidString}
	// header: x-task-id: {taskID}
	httpRequest := http.Request{
		Method: http.MethodGet,
		URL:    u,
		Header: map[string][]string{
			"Accept": {"application/json"},
		},
	}
	httpClient := http.Client{
		Timeout: getTimeOut,
	}
	httpResponse, err := httpClient.Do(&httpRequest)
	if err != nil {
		log.Errorf("access %s failed, method: %s", accessUrl, http.MethodGet)
		return cid.Undef, err
	}
	if httpResponse.StatusCode != http.StatusOK {
		log.Errorf("access %s failed, method: %s, status code: %d", accessUrl, http.MethodGet, httpResponse.StatusCode)
		return cid.Undef, fmt.Errorf("upload file slice failed, status code: %d", httpResponse.StatusCode)
	}
	var result bool
	b, err := io.ReadAll(httpResponse.Body)
	if err != nil {
		return cid.Undef, err
	}
	err = json.Unmarshal(b, &result)
	if err != nil {
		return cid.Undef, err
	}
	if result {
		return newCid, nil
	}

	httpRequest = http.Request{
		Method: http.MethodPost,
		URL:    u,
		Header: map[string][]string{
			"Accept":       {"application/json"},
			"Content-Type": {"application/octet-stream"},
			// "Content-Length": {strconv.Itoa(len(fileSlice))},
		},
		Body: io.NopCloser(bytes.NewReader(fileSlice)),
	}
	httpResponse, err = httpClient.Do(&httpRequest)
	if err != nil {
		return cid.Undef, err
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK && httpResponse.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(httpResponse.Body)
		message := string(b)
		log.Errorf("upload file slice failed, status code: %d, message: %s", httpResponse.StatusCode, message)
		return cid.Undef, fmt.Errorf("upload file slice failed, status code: %d, message: %s", httpResponse.StatusCode, message)
	}
	//

	return newCid, nil
}
