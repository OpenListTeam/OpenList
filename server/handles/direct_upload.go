package handles

import (
	"context"
	"net/url"

	"github.com/OpenListTeam/OpenList/v4/drivers/onedrive"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// FsGetDirectUploadURL returns the direct upload URL for OneDrive
func FsGetDirectUploadURL(c *gin.Context) {
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
	
	// Get storage for the path
	storage, err := fs.GetStorage(path, &fs.GetStoragesArgs{})
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	
	// Check if storage is OneDrive and has direct upload enabled
	onedriveDriver, ok := storage.(*onedrive.Onedrive)
	if !ok {
		common.ErrorStrResp(c, "Direct upload is only supported for OneDrive", 400)
		return
	}
	
	if !onedriveDriver.EnableDirectUpload {
		common.ErrorStrResp(c, "Direct upload is not enabled for this storage", 400)
		return
	}
	
	// Get directory object
	ctx := context.Background()
	dir, err := fs.Get(ctx, path, &fs.GetArgs{})
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	
	if !dir.IsDir() {
		common.ErrorStrResp(c, "Path is not a directory", 400)
		return
	}
	
	// Debug: log the paths being used
	// utils.Log.Debugf("[DirectUpload] DirPath: %s, FileName: %s, FullPath: %s", 
	//	dir.GetPath(), req.FileName, path)
	
	// Get upload URL
	uploadURL, err := onedriveDriver.GetDirectUploadURL(ctx, dir, req.FileName, req.FileSize)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	
	common.SuccessResp(c, gin.H{
		"upload_url": uploadURL,
		"chunk_size": onedriveDriver.ChunkSize * 1024 * 1024,
	})
}

// FsCheckDirectUpload checks if direct upload is enabled for current path
func FsCheckDirectUpload(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		path = "/"
	}
	
	// Decode path
	path, err := url.PathUnescape(path)
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
	
	// Get storage for the path
	storage, err := fs.GetStorage(path, &fs.GetStoragesArgs{})
	if err != nil {
		common.SuccessResp(c, gin.H{
			"enabled": false,
		})
		return
	}
	
	// Check if storage is OneDrive and has direct upload enabled
	onedriveDriver, ok := storage.(*onedrive.Onedrive)
	if !ok {
		common.SuccessResp(c, gin.H{
			"enabled": false,
		})
		return
	}
	
	common.SuccessResp(c, gin.H{
		"enabled": onedriveDriver.EnableDirectUpload,
	})
}
