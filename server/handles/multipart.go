package handles

import (
	"io"
	"net/url"
	stdpath "path"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

func FsMultipart(c *gin.Context) {
	action := c.Query("action")
	switch action {
	case "init":
		fsMultipartInit(c)
	case "upload":
		fsMultipartUpload(c)
	case "complete":
		fsMultipartComplete(c)
	default:
		common.ErrorStrResp(c, "invalid action", 400)
	}
}

func fsMultipartInit(c *gin.Context) {
	var req model.MultipartInitReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	path, err := url.PathUnescape(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	path, err = user.JoinPath(path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
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

	dstDirPath, fileName := stdpath.Split(path)
	mimetype := utils.GetMimeType(fileName)

	session, err := fs.MultipartSessionManager.InitMultipartUpload(
		c.Request.Context(),
		storage.GetStorage().ID,
		dstDirPath,
		fileName,
		req.FileSize,
		req.ChunkSize,
		mimetype,
	)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	common.SuccessResp(c, model.MultipartInitResp{
		UploadID:    session.UploadID,
		ChunkSize:   session.ChunkSize,
		TotalChunks: session.TotalChunks,
	})
}

func fsMultipartUpload(c *gin.Context) {
	uploadID := c.GetHeader("X-Upload-Id")
	if uploadID == "" {
		common.ErrorStrResp(c, "missing X-Upload-Id header", 400)
		return
	}

	chunkIndexStr := c.GetHeader("X-Chunk-Index")
	chunkIndex, err := strconv.Atoi(chunkIndexStr)
	if err != nil {
		common.ErrorStrResp(c, "invalid X-Chunk-Index header", 400)
		return
	}

	chunkSizeStr := c.GetHeader("X-Chunk-Size")
	chunkSize, err := strconv.ParseInt(chunkSizeStr, 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "invalid X-Chunk-Size header", 400)
		return
	}

	md5 := c.GetHeader("X-Chunk-Md5")

	resp, err := fs.MultipartSessionManager.UploadChunk(
		c.Request.Context(),
		uploadID,
		chunkIndex,
		chunkSize,
		c.Request.Body,
		md5,
	)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	io.ReadAll(c.Request.Body)
	c.Request.Body.Close()

	common.SuccessResp(c, resp)
}

func fsMultipartComplete(c *gin.Context) {
	var req model.MultipartCompleteReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	err := fs.MultipartSessionManager.CompleteMultipartUpload(
		c.Request.Context(),
		req.UploadID,
	)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	common.SuccessResp(c, gin.H{"success": true})
}
