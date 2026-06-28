package handles

import (
	"net/url"
	stdpath "path"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

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
