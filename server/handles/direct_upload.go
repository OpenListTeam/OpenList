package handles

import (
	"context"
	"io"
	"mime"
	"net/url"
	stdpath "path"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/directupload"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type FsGetDirectUploadInfoReq struct {
	Path        string `json:"path" form:"path"`
	FileName    string `json:"file_name" form:"file_name"`
	FileSize    int64  `json:"file_size" form:"file_size"`
	ContentType string `json:"content_type" form:"content_type"`
	Tool        string `json:"tool" form:"tool"`
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
	if req.ContentType != "" {
		if _, _, err := mime.ParseMediaType(req.ContentType); err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
	}
	ctx := context.WithValue(c, conf.DirectUploadContentTypeKey, req.ContentType)
	directUploadInfo, err := fs.GetDirectUploadInfo(ctx, req.Tool, path, req.FileName, req.FileSize, overwrite)
	if err != nil {
		if !overwrite && errs.IsObjectAlreadyExists(err) {
			common.ErrorStrResp(c, "file exists", 403)
			return
		}
		if errors.Is(err, errs.NotImplement) {
			// the driver has no direct upload path for this storage or policy,
			// report "not available" instead of an error so the client can
			// fall back to a regular upload
			common.SuccessResp(c, nil)
			return
		}
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, directUploadInfo)
}

type FsCompleteDirectUploadReq struct {
	Token string   `json:"token"`
	ETags []string `json:"etags"`
}

// FsCompleteDirectUpload finalizes a client-side upload that was announced by
// /fs/get_direct_upload_info. The client reports the part ETags it collected
// while uploading straight to the storage, and the driver takes care of the
// multipart completion and of any storage-specific callback.
func FsCompleteDirectUpload(c *gin.Context) {
	var req FsCompleteDirectUploadReq
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		common.ErrorResp(c, err, 400)
		return
	}
	token := req.Token
	if token == "" {
		// the URL handed out by the driver carries the token in its query
		token = c.Query("token")
	}
	if token == "" {
		common.ErrorStrResp(c, "missing upload token", 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	session, err := directupload.Get(token, user.ID)
	if err != nil {
		if errors.Is(err, directupload.ErrNotOwner) {
			common.ErrorResp(c, err, 403)
			return
		}
		common.ErrorResp(c, err, 404)
		return
	}
	if err = session.Finish(c.Request.Context(), req.ETags); err != nil {
		// keep the session so the client can retry the completion
		common.ErrorResp(c, err, 500)
		return
	}
	directupload.Delete(token)
	// The payload never reached this server, so the cached listing of the
	// destination directory has to be dropped for the new file to show up.
	if storage, actualPath, err := op.GetStorageAndActualPath(session.Path); err == nil {
		op.Cache.DeleteDirectory(storage, stdpath.Dir(actualPath))
	}
	common.SuccessResp(c, nil)
}
