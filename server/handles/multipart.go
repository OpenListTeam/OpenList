package handles

import (
	"io"
	"net/url"
	stdpath "path"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

func FsMultipart(c *gin.Context) {
	action := c.Query("action")
	switch action {
	case "upload":
		fsMultipartUpload(c)
	case "complete":
		fsMultipartComplete(c)
	default:
		common.ErrorStrResp(c, "invalid action, must be 'upload' or 'complete'", 400)
	}
}

func fsMultipartUpload(c *gin.Context) {
	defer func() {
		if n, _ := io.ReadFull(c.Request.Body, []byte{0}); n == 1 {
			_, _ = utils.CopyWithBuffer(io.Discard, c.Request.Body)
		}
		_ = c.Request.Body.Close()
	}()

	// Get File-Path header (already validated by FsUp middleware)
	path := c.GetHeader("File-Path")
	path, err := url.PathUnescape(path)
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

	dir, name := stdpath.Split(path)
	if shouldIgnoreSystemFile(name) {
		common.ErrorStrResp(c, errs.IgnoredSystemFile.Error(), 403)
		return
	}

	// Get upload ID (optional for first chunk)
	uploadID := c.GetHeader("X-Upload-Id")

	// Get chunk index (required)
	chunkIndexStr := c.GetHeader("X-Chunk-Index")
	chunkIndex, err := strconv.Atoi(chunkIndexStr)
	if err != nil {
		common.ErrorStrResp(c, "invalid or missing X-Chunk-Index header", 400)
		return
	}

	// Get chunk size from Content-Length
	chunkSize := c.Request.ContentLength
	if chunkSize <= 0 {
		common.ErrorStrResp(c, "missing Content-Length header", 400)
		return
	}

	// For first chunk (no uploadID), need file size and chunk size
	var session *model.MultipartUploadSession
	if uploadID == "" {
		// First chunk - initialize session
		fileSizeStr := c.GetHeader("X-File-Size")
		fileSize, err := strconv.ParseInt(fileSizeStr, 10, 64)
		if err != nil || fileSize <= 0 {
			common.ErrorStrResp(c, "invalid or missing X-File-Size header for first chunk", 400)
			return
		}

		expectedChunkSizeStr := c.GetHeader("X-Chunk-Size")
		expectedChunkSize, err := strconv.ParseInt(expectedChunkSizeStr, 10, 64)
		if err != nil || expectedChunkSize <= 0 {
			common.ErrorStrResp(c, "invalid or missing X-Chunk-Size header for first chunk", 400)
			return
		}

		overwrite := c.GetHeader("Overwrite") != "false"

		// Check if file exists when not overwriting
		if !overwrite {
			if res, _ := fs.Get(c.Request.Context(), path, &fs.GetArgs{NoLog: true}); res != nil {
				common.ErrorStrResp(c, "file already exists and overwrite is disabled", 403)
				return
			}
		}

		mimetype := c.GetHeader("Content-Type")
		if mimetype == "" || mimetype == "application/octet-stream" {
			mimetype = utils.GetMimeType(name)
		}

		session, err = fs.MultipartSessionManager.InitOrGetSession(
			"",
			dir,
			name,
			fileSize,
			expectedChunkSize,
			mimetype,
			overwrite,
		)
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
	} else {
		// Subsequent chunk - get existing session
		session, err = fs.MultipartSessionManager.InitOrGetSession(
			uploadID,
			"", "", 0, 0, "", false,
		)
		if err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
	}

	// Upload chunk
	resp, err := fs.MultipartSessionManager.UploadChunk(
		session.UploadID,
		chunkIndex,
		chunkSize,
		c.Request.Body,
	)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	common.SuccessResp(c, resp)
}

func fsMultipartComplete(c *gin.Context) {
	uploadID := c.GetHeader("X-Upload-Id")
	if uploadID == "" {
		common.ErrorStrResp(c, "missing X-Upload-Id header", 400)
		return
	}

	asTask := c.GetHeader("As-Task") == "true"

	t, err := fs.MultipartSessionManager.CompleteUpload(
		c.Request.Context(),
		uploadID,
		asTask,
	)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	if t == nil {
		common.SuccessResp(c, gin.H{"success": true})
		return
	}

	common.SuccessResp(c, gin.H{
		"success": true,
		"task":    getTaskInfo(t),
	})
}
