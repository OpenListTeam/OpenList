package handles

import (
	"net/url"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
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
	overwrite := c.GetHeader("Overwrite") != "false"
	if !overwrite {
		if res, _ := fs.Get(c.Request.Context(), path, &fs.GetArgs{NoLog: true}); res != nil {
			common.ErrorStrResp(c, "file exists", 403)
			return
		}
	}
	directUploadInfo, err := fs.GetDirectUploadInfo(c, req.Tool, path, req.FileName, req.FileSize)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, directUploadInfo)
}
