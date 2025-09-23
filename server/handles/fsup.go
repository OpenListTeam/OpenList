package handles

import (
	"fmt"
	"io"
	"net/url"
	stdpath "path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/model/reqres"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

func getLastModified(c *gin.Context) time.Time {
	now := time.Now()
	lastModifiedStr := c.GetHeader("Last-Modified")
	lastModifiedMillisecond, err := strconv.ParseInt(lastModifiedStr, 10, 64)
	if err != nil {
		return now
	}
	lastModified := time.UnixMilli(lastModifiedMillisecond)
	return lastModified
}

func FsStream(c *gin.Context) {
	defer func() {
		if n, _ := io.ReadFull(c.Request.Body, []byte{0}); n == 1 {
			_, _ = utils.CopyWithBuffer(io.Discard, c.Request.Body)
		}
		_ = c.Request.Body.Close()
	}()
	path := c.GetHeader("File-Path")
	path, err := url.PathUnescape(path)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	asTask := c.GetHeader("As-Task") == "true"
	overwrite := c.GetHeader("Overwrite") != "false"
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	path, err = user.JoinPath(path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	if !overwrite {
		if res, _ := fs.Get(c.Request.Context(), path, &fs.GetArgs{NoLog: true}); res != nil {
			common.ErrorStrResp(c, "file exists", 403)
			return
		}
	}
	dir, name := stdpath.Split(path)
	// 如果请求头 Content-Length 和 X-File-Size 都没有，则 size=-1，表示未知大小的流式上传
	size := c.Request.ContentLength
	if size < 0 {
		sizeStr := c.GetHeader("X-File-Size")
		if sizeStr != "" {
			size, err = strconv.ParseInt(sizeStr, 10, 64)
			if err != nil {
				common.ErrorResp(c, err, 400)
				return
			}
		}
	}
	h := make(map[*utils.HashType]string)
	if md5 := c.GetHeader("X-File-Md5"); md5 != "" {
		h[utils.MD5] = md5
	}
	if sha1 := c.GetHeader("X-File-Sha1"); sha1 != "" {
		h[utils.SHA1] = sha1
	}
	if sha256 := c.GetHeader("X-File-Sha256"); sha256 != "" {
		h[utils.SHA256] = sha256
	}
	mimetype := c.GetHeader("Content-Type")
	if len(mimetype) == 0 {
		mimetype = utils.GetMimeType(name)
	}
	s := &stream.FileStream{
		Obj: &model.Object{
			Name:     name,
			Size:     size,
			Modified: getLastModified(c),
			HashInfo: utils.NewHashInfoByMap(h),
		},
		Reader:       c.Request.Body,
		Mimetype:     mimetype,
		WebPutAsTask: asTask,
	}
	var t task.TaskExtensionInfo
	if asTask {
		t, err = fs.PutAsTask(c.Request.Context(), dir, s)
	} else {
		err = fs.PutDirectly(c.Request.Context(), dir, s, true)
	}
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if t == nil {
		common.SuccessResp(c)
		return
	}
	common.SuccessResp(c, gin.H{
		"task": getTaskInfo(t),
	})
}

func FsForm(c *gin.Context) {
	defer func() {
		if n, _ := io.ReadFull(c.Request.Body, []byte{0}); n == 1 {
			_, _ = utils.CopyWithBuffer(io.Discard, c.Request.Body)
		}
		_ = c.Request.Body.Close()
	}()
	path := c.GetHeader("File-Path")
	path, err := url.PathUnescape(path)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	asTask := c.GetHeader("As-Task") == "true"
	overwrite := c.GetHeader("Overwrite") != "false"
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	path, err = user.JoinPath(path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	if !overwrite {
		if res, _ := fs.Get(c.Request.Context(), path, &fs.GetArgs{NoLog: true}); res != nil {
			common.ErrorStrResp(c, "file exists", 403)
			return
		}
	}
	storage, err := fs.GetStorage(path, &fs.GetStoragesArgs{})
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if storage.Config().NoUpload {
		common.ErrorStrResp(c, "Current storage doesn't support upload", 405)
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	f, err := file.Open()
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	defer f.Close()
	dir, name := stdpath.Split(path)
	h := make(map[*utils.HashType]string)
	if md5 := c.GetHeader("X-File-Md5"); md5 != "" {
		h[utils.MD5] = md5
	}
	if sha1 := c.GetHeader("X-File-Sha1"); sha1 != "" {
		h[utils.SHA1] = sha1
	}
	if sha256 := c.GetHeader("X-File-Sha256"); sha256 != "" {
		h[utils.SHA256] = sha256
	}
	mimetype := file.Header.Get("Content-Type")
	if len(mimetype) == 0 {
		mimetype = utils.GetMimeType(name)
	}
	s := &stream.FileStream{
		Obj: &model.Object{
			Name:     name,
			Size:     file.Size,
			Modified: getLastModified(c),
			HashInfo: utils.NewHashInfoByMap(h),
		},
		Reader:       f,
		Mimetype:     mimetype,
		WebPutAsTask: asTask,
	}
	var t task.TaskExtensionInfo
	if asTask {
		s.Reader = struct {
			io.Reader
		}{f}
		t, err = fs.PutAsTask(c.Request.Context(), dir, s)
	} else {
		err = fs.PutDirectly(c.Request.Context(), dir, s, true)
	}
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if t == nil {
		common.SuccessResp(c)
		return
	}
	common.SuccessResp(c, gin.H{
		"task": getTaskInfo(t),
	})
}

// 分片上传流程如下
// 1. 客户端调用FsUpHash获取上传所需的信息(目前主要是hash信息)
// 2. 根据获取到的hash信息，客户端调用FsPreup上传必要的参数，获取分片大小，及需上传的分片列表
// 3. 客户端根据分片列表进行分片上传，如果分片是第一个，且需要sliceHash，那么需要把所有分片的hash带上
// 4. 如果中途出现问题，可以重新进行分片上传流程，后端根据记录的信息进行恢复
// 如果网盘不支持分片上传，则会进行本地中转，对客户端来说，仍然是分片上传

// FsUpInfo 获取上传所需的信息
func FsUpInfo(c *gin.Context) {
	storage := c.Request.Context().Value(conf.StorageKey)

	uh := &model.UploadInfo{
		SliceHashNeed: false,
		HashMd5Need:   true,
	}
	switch s := storage.(type) {
	case driver.IUploadInfo:
		uh = s.GetUploadInfo()
	}
	common.SuccessResp(c, uh)
}

// FsPreup 预上传
func FsPreup(c *gin.Context) {
	req := &reqres.PreupReq{}
	err := c.ShouldBindJSON(req)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("invalid request body: %w", err), 400)
		return
	}

	if req.Name == "" {
		common.ErrorResp(c, fmt.Errorf("file name is required"), 400)
		return
	}
	if req.Size <= 0 {
		common.ErrorResp(c, fmt.Errorf("file size must be greater than 0"), 400)
		return
	}

	storage := c.Request.Context().Value(conf.StorageKey).(driver.Driver)
	path := c.Request.Context().Value(conf.PathKey).(string)
	if !req.Overwrite {
		fullPath := utils.FixAndCleanPath(stdpath.Join(path, req.Name))
		if res, _ := fs.Get(c.Request.Context(), fullPath, &fs.GetArgs{NoLog: true}); res != nil {
			common.ErrorStrResp(c, "file exists", 403)
			return
		}
	}

	res, err := fs.Preup(c.Request.Context(), storage, path, req)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("preup failed: %w", err), 500)
		return
	}
	common.SuccessResp(c, res)
}

// FsUpSlice 流式上传分片 - 使用PUT方法进行流式上传，避免表单上传的内存占用
func FsUpSlice(c *gin.Context) {
	defer func() {
		if n, _ := io.ReadFull(c.Request.Body, []byte{0}); n == 1 {
			_, _ = utils.CopyWithBuffer(io.Discard, c.Request.Body)
		}
		_ = c.Request.Body.Close()
	}()
	taskID := c.GetHeader("X-Task-ID")
	if taskID == "" {
		common.ErrorResp(c, fmt.Errorf("X-Task-ID header is required"), 400)
		return
	}

	sliceNumStr := c.GetHeader("X-Slice-Num")
	if sliceNumStr == "" {
		common.ErrorResp(c, fmt.Errorf("X-Slice-Num header is required"), 400)
		return
	}

	sliceNum, err := strconv.ParseUint(sliceNumStr, 10, 32)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("invalid X-Slice-Num: %w", err), 400)
		return
	}

	sliceHash := c.GetHeader("X-Slice-Hash")

	req := &reqres.UploadSliceReq{
		TaskID:    taskID,
		SliceHash: sliceHash,
		SliceNum:  uint(sliceNum),
	}

	reader := c.Request.Body
	if reader == nil {
		common.ErrorResp(c, fmt.Errorf("request body is required"), 400)
		return
	}

	storage := c.Request.Context().Value(conf.StorageKey).(driver.Driver)

	err = fs.UploadSlice(c.Request.Context(), storage, req, reader)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("upload slice failed: %w", err), 500)
		return
	}

	common.SuccessResp(c)
}

// FsUpSliceComplete 上传分片完成
func FsUpSliceComplete(c *gin.Context) {
	req := &reqres.UploadSliceCompleteReq{}
	err := c.ShouldBindJSON(req)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("invalid request body: %w", err), 400)
		return
	}

	if req.TaskID == "" {
		common.ErrorResp(c, fmt.Errorf("task_id is required"), 400)
		return
	}

	storage := c.Request.Context().Value(conf.StorageKey).(driver.Driver)
	rsp, err := fs.SliceUpComplete(c.Request.Context(), storage, req.TaskID)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("slice upload complete failed: %w", err), 500)
		return
	}
	common.SuccessResp(c, rsp)
}
