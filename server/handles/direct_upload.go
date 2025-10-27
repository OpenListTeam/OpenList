package handles

import (
	"context"
	"errors"
	"net/url"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// FsGetDirectUploadInfo returns the direct upload info if supported by the driver
// If the driver does not support direct upload, returns null for upload_info
func FsGetDirectUploadInfo(c *gin.Context) {
	var req struct {
		Path     string `json:"path" form:"path"`
		FileName string `json:"file_name" form:"file_name"`
		FileSize int64  `json:"file_size" form:"file_size"`
	}
	
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
	
	// Get storage and actual path (after removing mount path prefix)
	storage, actualPath, err := fs.GetStorageAndActualPath(path)
	if err != nil {
		// If no storage found, direct upload is not supported
		common.SuccessResp(c, gin.H{
			"upload_info": nil,
		})
		return
	}
	
	// Check if storage implements DirectUploader interface
	directUploader, ok := storage.(driver.DirectUploader)
	if !ok {
		// Driver does not support direct upload
		common.SuccessResp(c, gin.H{
			"upload_info": nil,
		})
		return
	}
	
	// Get directory object using actual path
	ctx := context.Background()
	dir, err := fs.GetByActualPath(ctx, storage, actualPath)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	
	if !dir.IsDir() {
		common.ErrorStrResp(c, "Path is not a directory", 400)
		return
	}
	
	// Get direct upload info using actual path
	uploadInfo, err := directUploader.GetDirectUploadInfo(ctx, dir, actualPath, req.FileName, req.FileSize)
	if err != nil {
		// Check if driver returned NotImplement error (direct upload not enabled)
		if errors.Is(err, errs.NotImplement) {
			common.SuccessResp(c, gin.H{
				"upload_info": nil,
			})
			return
		}
		// Other errors
		common.ErrorResp(c, err, 500)
		return
	}
	
	common.SuccessResp(c, gin.H{
		"upload_info": uploadInfo,
	})
}
