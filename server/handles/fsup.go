package handles

import (
	"io"
	"net/url"
	stdpath "path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
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

// shouldIgnoreSystemFile checks if the filename should be ignored based on settings
func shouldIgnoreSystemFile(filename string) bool {
	if setting.GetBool(conf.IgnoreSystemFiles) {
		return utils.IsSystemFile(filename)
	}
	return false
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
	// Check if system file should be ignored
	if shouldIgnoreSystemFile(name) {
		common.ErrorStrResp(c, errs.IgnoredSystemFile.Error(), 403)
		return
	}
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
		err = fs.PutDirectly(c.Request.Context(), dir, s)
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
	// Check if system file should be ignored
	if shouldIgnoreSystemFile(name) {
		common.ErrorStrResp(c, errs.IgnoredSystemFile.Error(), 403)
		return
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
		err = fs.PutDirectly(c.Request.Context(), dir, s)
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

type FsGetDirectUploadInfoReq struct {
	Path     string `json:"path" form:"path"`
	FileName string `json:"file_name" form:"file_name"`
	FileSize int64  `json:"file_size" form:"file_size"`
	Tool     string `json:"tool" form:"tool"`
}

type FsCompleteDirectUploadReq struct {
	Path        string `json:"path" form:"path"`
	FileName    string `json:"file_name" form:"file_name"`
	Tool        string `json:"tool" form:"tool"`
	UploadToken string `json:"upload_token" form:"upload_token"`
}

func resolveDirectUploadDir(c *gin.Context, rawPath, fileName string) (string, error) {
	path, err := url.PathUnescape(rawPath)
	if err != nil {
		return "", err
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	path, err = user.JoinPath(path)
	if err != nil {
		return "", err
	}
	if err := checkRelativePath(fileName); err != nil {
		return "", err
	}
	return path, nil
}

func resolveDirectUploadFile(c *gin.Context, rawPath, fileName string) (string, string, error) {
	filePath := c.GetHeader("File-Path")
	if filePath != "" {
		path, err := url.PathUnescape(filePath)
		if err != nil {
			return "", "", err
		}
		user := c.Request.Context().Value(conf.UserKey).(*model.User)
		path, err = user.JoinPath(path)
		if err != nil {
			return "", "", err
		}
		name := stdpath.Base(path)
		if err := checkRelativePath(name); err != nil {
			return "", "", err
		}
		return stdpath.Dir(path), name, nil
	}
	path, err := resolveDirectUploadDir(c, rawPath, fileName)
	return path, fileName, err
}

func checkDirectUploadWritePermission(c *gin.Context, parentPath string) error {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	parentMeta, err := op.GetNearestMeta(parentPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return err
	}
	if !user.CanWriteContent() && !common.CanWriteContentBypassUserPerms(parentMeta, parentPath) {
		return errs.PermissionDenied
	}
	if !common.CanWrite(user, parentMeta, parentPath) {
		return errs.PermissionDenied
	}
	return nil
}

func respondDirectUploadPermissionError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errs.PermissionDenied) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return true
	}
	common.ErrorResp(c, err, 500, true)
	return true
}

// FsGetDirectUploadInfo returns the direct upload info if supported by the driver
// If the driver does not support direct upload, returns null for upload_info
func FsGetDirectUploadInfo(c *gin.Context) {
	var req FsGetDirectUploadInfoReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	path, fileName, err := resolveDirectUploadFile(c, req.Path, req.FileName)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	if respondDirectUploadPermissionError(c, checkDirectUploadWritePermission(c, path)) {
		return
	}
	overwrite := c.GetHeader("Overwrite") != "false"
	dstPath := stdpath.Join(path, fileName)
	if !overwrite {
		res, err := fs.Get(c.Request.Context(), dstPath, &fs.GetArgs{NoLog: true})
		if err != nil && !errs.IsObjectNotFound(err) {
			common.ErrorResp(c, err, 500)
			return
		}
		if res != nil {
			common.ErrorStrResp(c, "file exists", 403)
			return
		}
	}
	directUploadInfo, err := fs.GetDirectUploadInfo(c, req.Tool, path, fileName, req.FileSize, overwrite)
	if err != nil {
		if !overwrite && errs.IsObjectAlreadyExists(err) {
			common.ErrorStrResp(c, "file exists", 403)
			return
		}
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, directUploadInfo)
}

// FsCompleteDirectUpload commits a client-side upload session after the client
// has uploaded the file bytes directly to the storage provider.
func FsCompleteDirectUpload(c *gin.Context) {
	var req FsCompleteDirectUploadReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	path, fileName, err := resolveDirectUploadFile(c, req.Path, req.FileName)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	if req.UploadToken == "" {
		common.ErrorStrResp(c, "upload_token is required", 400)
		return
	}
	if respondDirectUploadPermissionError(c, checkDirectUploadWritePermission(c, path)) {
		return
	}
	obj, err := fs.CompleteDirectUpload(c.Request.Context(), req.Tool, path, fileName, req.UploadToken)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, obj)
}
