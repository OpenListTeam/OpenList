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
	log "github.com/sirupsen/logrus"
)

type FsGetDirectUploadInfoReq struct {
	Path     string `json:"path" form:"path"`
	FileName string `json:"file_name" form:"file_name"`
	FileSize int64  `json:"file_size" form:"file_size"`
	Tool     string `json:"tool" form:"tool"`
}

// FsGetDirectUploadInfo returns the direct upload info if supported by the driver
// If the driver does not support direct upload, returns null for upload_info
func FsGetDirectUploadInfo(c *gin.Context) {
	var req FsGetDirectUploadInfoReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	// Decode path
	path, err := url.PathUnescape(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	// Get user and join path
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	path, err = user.JoinPath(path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	if err := checkRelativePath(req.FileName); err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	// Resolve the destination once so permission checks and the issued upload
	// capability cannot target different paths.
	dstPath := stdpath.Join(path, req.FileName)
	path = stdpath.Dir(dstPath)
	req.FileName = stdpath.Base(dstPath)
	parentMeta, err := op.GetNearestMeta(path)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	if !user.CanWriteContent() && !common.CanWriteContentBypassUserPerms(parentMeta, path) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	if !common.CanWrite(user, parentMeta, path) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	// A single-segment file name can still name a nested mount point.
	parentMountPath, err := op.GetStorageVirtualMountPath(path)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	targetMountPath, err := op.GetStorageVirtualMountPath(dstPath)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if parentMountPath != targetMountPath {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	overwrite := c.GetHeader("Overwrite") != "false"
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
	directUploadInfo, err := fs.GetDirectUploadInfo(c, req.Tool, path, req.FileName, req.FileSize, overwrite)
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

type FsCompleteDirectUploadReq struct {
	Path        string `json:"path" form:"path"`
	FileName    string `json:"file_name" form:"file_name"`
	Tool        string `json:"tool" form:"tool"`
	UploadToken string `json:"upload_token" form:"upload_token"`
}

// FsCompleteDirectUpload commits a client-side upload session after the client
// has uploaded the file bytes directly to the storage provider.
func FsCompleteDirectUpload(c *gin.Context) {
	var req FsCompleteDirectUploadReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.UploadToken == "" {
		common.ErrorStrResp(c, "upload_token is required", 400)
		return
	}
	// Decode path
	path, err := url.PathUnescape(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	// Get user and join path
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	path, err = user.JoinPath(path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	if err := checkRelativePath(req.FileName); err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	// Resolve the destination once so permission checks cannot target different paths.
	dstPath := stdpath.Join(path, req.FileName)
	path = stdpath.Dir(dstPath)
	req.FileName = stdpath.Base(dstPath)
	parentMeta, err := op.GetNearestMeta(path)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	if !user.CanWriteContent() && !common.CanWriteContentBypassUserPerms(parentMeta, path) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	if !common.CanWrite(user, parentMeta, path) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	parentMountPath, err := op.GetStorageVirtualMountPath(path)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	targetMountPath, err := op.GetStorageVirtualMountPath(dstPath)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if parentMountPath != targetMountPath {
		log.Warnf("[DirectUpload] cross-mount completion rejected: user=%s, path=%s, file=%s", user.Username, path, req.FileName)
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	obj, err := fs.CompleteDirectUpload(c.Request.Context(), req.Tool, path, req.FileName, req.UploadToken)
	if err != nil {
		log.Errorf("[DirectUpload] complete failed: user=%s, path=%s, file=%s, tool=%s, err=%v", user.Username, path, req.FileName, req.Tool, err)
		common.ErrorResp(c, err, 500)
		return
	}
	log.Infof("[DirectUpload] completed successfully: user=%s, path=%s, file=%s, tool=%s", user.Username, path, req.FileName, req.Tool)
	common.SuccessResp(c, obj)
}

