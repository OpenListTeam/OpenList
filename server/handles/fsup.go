package handles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	stdpath "path"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
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
	generateSeed := shouldGenerateUploadSeed(c, dir)
	if generateSeed && asTask {
		common.ErrorStrResp(c, "seed sidecar generation requires synchronous upload", 400)
		return
	}
	var seedHasher *torrent.HashWriter
	var uploadReader io.Reader = c.Request.Body
	if generateSeed {
		seedHasher = torrent.NewHashWriter(seedPieceSize(c), seedPieceSize(c))
		uploadReader = io.TeeReader(c.Request.Body, seedHasher)
	}
	s := &stream.FileStream{
		Obj: &model.Object{
			Name:     name,
			Size:     size,
			Modified: getLastModified(c),
			HashInfo: utils.NewHashInfoByMap(h),
		},
		Reader:       uploadReader,
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
	if generateSeed {
		if err = writeUploadSeedSidecar(c, dir, name, size, seedHasher); err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
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
	generateSeed := shouldGenerateUploadSeed(c, dir)
	if generateSeed && asTask {
		common.ErrorStrResp(c, "seed sidecar generation requires synchronous upload", 400)
		return
	}
	var seedHasher *torrent.HashWriter
	var uploadReader io.Reader = f
	if generateSeed {
		seedHasher = torrent.NewHashWriter(seedPieceSize(c), seedPieceSize(c))
		uploadReader = io.TeeReader(f, seedHasher)
	}
	s := &stream.FileStream{
		Obj: &model.Object{
			Name:     name,
			Size:     file.Size,
			Modified: getLastModified(c),
			HashInfo: utils.NewHashInfoByMap(h),
		},
		Reader:       uploadReader,
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
	if generateSeed {
		if err = writeUploadSeedSidecar(c, dir, name, file.Size, seedHasher); err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
	}
	if t == nil {
		common.SuccessResp(c)
		return
	}
	common.SuccessResp(c, gin.H{
		"task": getTaskInfo(t),
	})
}

func shouldGenerateUploadSeed(c *gin.Context, path string) bool {
	if strings.TrimSpace(c.GetHeader("X-Seed-Sidecars")) != "" {
		return true
	}
	policy := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Generate-Seed")))
	if policy != "" && policy != "inherit" {
		return policy == "on" || policy == "true" || policy == "1"
	}
	if storage := op.GetBalancedStorage(path); storage != nil {
		policy = strings.ToLower(strings.TrimSpace(storage.GetStorage().SeedPolicy))
	}
	if policy == "" || policy == "inherit" {
		policy = strings.ToLower(setting.GetStr(conf.SeedAutoGeneratePolicy, "off"))
	}
	return (policy == "on" || policy == "true" || policy == "1") && configuredSeedFormats() != ""
}

func seedPieceSize(c *gin.Context) int64 {
	for _, format := range strings.Split(strings.ToLower(c.GetHeader("X-Seed-Sidecars")), ",") {
		if strings.TrimSpace(format) == "cas" {
			return torrent.DefaultPieceSize
		}
	}
	value := c.GetHeader("X-Seed-Piece-Size")
	if value == "" {
		return torrent.DefaultPieceSize
	}
	size, err := strconv.ParseInt(value, 10, 64)
	if err != nil || size <= 0 || size > 1<<30 {
		return torrent.DefaultPieceSize
	}
	return size
}

func configuredSeedFormats() string {
	policies := make(map[string]string)
	if err := json.Unmarshal([]byte(setting.GetStr(conf.SeedFormatPolicies)), &policies); err != nil {
		return ""
	}
	formats := make([]string, 0, 3)
	for _, format := range []string{"oss", "torrent", "cas"} {
		if strings.EqualFold(strings.TrimSpace(policies[format]), "on") {
			formats = append(formats, format)
		}
	}
	return strings.Join(formats, ",")
}

func writeUploadSeedSidecar(c *gin.Context, dir, name string, expectedSize int64, hasher *torrent.HashWriter) error {
	if hasher == nil {
		return nil
	}
	hasher.Finish()
	if expectedSize >= 0 && hasher.GetTotalWritten() != expectedSize {
		return fmt.Errorf("seed sidecar requires a complete stream: read %d of %d bytes", hasher.GetTotalWritten(), expectedSize)
	}
	seed := torrent.NewSeed(name, "OpenList", seedPieceSize(c))
	formatsHeader := strings.TrimSpace(c.GetHeader("X-Seed-Sidecars"))
	if formatsHeader == "" {
		formatsHeader = strings.TrimSpace(c.GetHeader("X-Seed-Format"))
	}
	if formatsHeader == "" {
		formatsHeader = configuredSeedFormats()
	}
	formats, err := normalizedSeedFormats(SeedGenerateReq{Formats: strings.Split(formatsHeader, ",")})
	if err != nil {
		return err
	}
	matrix := SeedHashMatrix{}
	if rawMatrix := strings.TrimSpace(c.GetHeader("X-Seed-Hash-Matrix")); rawMatrix != "" {
		if err = json.Unmarshal([]byte(rawMatrix), &matrix); err != nil {
			return fmt.Errorf("invalid seed hash matrix: %w", err)
		}
	}
	matrix = normalizedSeedMatrix(matrix, formats)
	seedFile := hasher.BuildSeedFile(name, getLastModified(c).UTC().Format(time.RFC3339))
	applySeedMatrix(&seedFile, matrix)
	seed.Files = []torrent.SeedFile{seedFile}
	seen := make(map[string]struct{}, 3)
	for _, rawFormat := range formats {
		format := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(rawFormat), "."))
		if format == "bt" {
			format = "torrent"
		}
		if _, exists := seen[format]; exists {
			continue
		}
		seen[format] = struct{}{}
		data, err := encodeGeneratedSeed(seed, format, hasher.GetPieceHashes())
		if err != nil {
			return fmt.Errorf("generate %s seed sidecar: %w", format, err)
		}
		sidecar := &stream.FileStream{
			Ctx:    c.Request.Context(),
			Obj:    &model.Object{Name: name + "." + format, Size: int64(len(data)), Modified: time.Now()},
			Reader: bytes.NewReader(data), Mimetype: "application/octet-stream",
		}
		if err = fs.PutDirectly(c.Request.Context(), dir, sidecar, true); err != nil {
			return fmt.Errorf("upload %s seed sidecar: %w", format, err)
		}
	}
	return nil
}
