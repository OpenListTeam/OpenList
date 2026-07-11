package handles

import (
	"errors"
	"io"
	"net/url"
	stdpath "path"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/multipart"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

const multipartMinChunkSize = int64(1) << 20 // 1MB

// multipartChunkSize resolves the effective chunk size. The admin setting is
// the ceiling: a client may suggest a smaller chunk via X-Chunk-Size but never
// a larger one — the server buffers a window of several chunks per session, so
// an unbounded client suggestion would translate directly into server-side
// disk usage.
func multipartChunkSize(requested int64) int64 {
	ceiling := int64(setting.GetInt(conf.MultipartChunkSize, 10)) << 20
	if ceiling < multipartMinChunkSize {
		ceiling = multipartMinChunkSize
	}
	size := ceiling
	if requested > 0 && requested < size {
		size = requested
	}
	if size < multipartMinChunkSize {
		size = multipartMinChunkSize
	}
	return size
}

type MultipartInitResp struct {
	multipart.SessionSnapshot
	Resumed bool `json:"resumed"`
}

// MultipartInit creates (or resumes) a multipart upload session and starts its
// upload pipeline. Headers mirror FsStream (fsup.go).
func MultipartInit(c *gin.Context) {
	if !setting.GetBool(conf.MultipartEnabled) {
		common.ErrorStrResp(c, "multipart upload is disabled", 403)
		return
	}
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
	size, err := strconv.ParseInt(c.GetHeader("X-File-Size"), 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "multipart upload requires a valid X-File-Size header", 400)
		return
	}
	if size <= 0 {
		common.ErrorStrResp(c, "multipart upload requires a positive X-File-Size; upload empty files via /fs/put", 400)
		return
	}
	var requestedChunkSize int64
	if v := c.GetHeader("X-Chunk-Size"); v != "" {
		requestedChunkSize, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
	}
	overwrite := c.GetHeader("Overwrite") != "false"
	if !overwrite {
		if res, _ := fs.Get(c.Request.Context(), path, &fs.GetArgs{NoLog: true}); res != nil {
			common.ErrorStrResp(c, "file exists", 403)
			return
		}
	}
	dir, name := stdpath.Split(path)
	if shouldIgnoreSystemFile(name) {
		common.ErrorStrResp(c, errs.IgnoredSystemFile.Error(), 403)
		return
	}
	// fail fast on unusable destinations instead of letting the pipeline discover it
	storage, _, err := op.GetStorageAndActualPath(dir)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if storage.Config().NoUpload {
		common.ErrorResp(c, errs.UploadNotSupported, 405)
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
	mimetype := c.GetHeader("Content-Type")
	if len(mimetype) == 0 {
		mimetype = utils.GetMimeType(name)
	}
	snap, resumed, err := multipart.DefaultManager.Init(multipart.InitReq{
		User:      user,
		Path:      path,
		Size:      size,
		ChunkSize: multipartChunkSize(requestedChunkSize),
		Mimetype:  mimetype,
		Modified:  getLastModified(c),
		Hashes:    h,
	})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, MultipartInitResp{SessionSnapshot: snap, Resumed: resumed})
}

// MultipartChunk ingests one chunk. Chunks are idempotent and may be sent
// concurrently and out of order within the receiving window.
// code 429 = window full (flow control, retry after a short delay),
// code 409 = the same chunk is already in flight on another connection.
func MultipartChunk(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	id := c.GetHeader("X-Upload-Id")
	idx, err := strconv.Atoi(c.GetHeader("X-Chunk-Index"))
	if err != nil {
		common.ErrorStrResp(c, "invalid X-Chunk-Index header", 400)
		return
	}
	snap, err := multipart.DefaultManager.Chunk(user, id, idx, c.Request.Body)
	// Answer only after the request body is consumed — on EVERY path. Flow
	// control (429), absorbed chunks and validation errors would otherwise
	// respond while the browser is still streaming the body, which it reports
	// as a network error and which poisons its connection pool. A rejected
	// chunk gets resent anyway, so draining costs no extra round trip. The
	// drain is bounded so a malformed request cannot pin the handler.
	limit := multipartChunkSize(0) + 64*1024
	if snap.ChunkSize > 0 {
		limit = snap.ChunkSize + 64*1024
	}
	_, _ = utils.CopyWithBuffer(io.Discard, io.LimitReader(c.Request.Body, limit))
	if err != nil {
		common.ErrorWithDataResp(c, err, multipartErrCode(err), snap)
		return
	}
	common.SuccessResp(c, snap)
}

// MultipartComplete waits for the pipeline outcome and reports it, mirroring
// how /fs/put only responds once the driver upload finished.
func MultipartComplete(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	id := c.GetHeader("X-Upload-Id")
	snap, err := multipart.DefaultManager.Complete(c.Request.Context(), user, id)
	if err != nil {
		common.ErrorWithDataResp(c, err, multipartErrCode(err), snap)
		return
	}
	common.SuccessResp(c, snap)
}

// MultipartStatus looks a session up by upload_id, or by path+size so an
// interrupted client can discover a resumable session.
func MultipartStatus(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if id := c.Query("upload_id"); id != "" {
		snap, err := multipart.DefaultManager.Status(user, id)
		if err != nil {
			common.ErrorResp(c, err, multipartErrCode(err))
			return
		}
		common.SuccessResp(c, snap)
		return
	}
	path, err := user.JoinPath(c.Query("path"))
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	size, err := strconv.ParseInt(c.Query("size"), 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "status lookup requires upload_id, or path and size", 400)
		return
	}
	snap, err := multipart.DefaultManager.Find(user, path, size)
	if err != nil {
		common.ErrorResp(c, err, multipartErrCode(err))
		return
	}
	common.SuccessResp(c, snap)
}

// MultipartAbort cancels the pipeline and discards the session.
func MultipartAbort(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if err := multipart.DefaultManager.Abort(user, c.GetHeader("X-Upload-Id")); err != nil {
		common.ErrorResp(c, err, multipartErrCode(err))
		return
	}
	common.SuccessResp(c)
}

func multipartErrCode(err error) int {
	switch {
	case errors.Is(err, multipart.ErrOutOfWindow):
		return 429
	case errors.Is(err, multipart.ErrChunkInFlight):
		return 409
	case errors.Is(err, multipart.ErrSessionNotFound):
		return 404
	case errors.Is(err, multipart.ErrNotOwner):
		return 403
	default:
		return 400
	}
}
