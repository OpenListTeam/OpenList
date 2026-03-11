package handles

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	stdpath "path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
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
	s := &stream.FileStream{
		Obj: &model.Object{
			Name:     name,
			Size:     size,
			Modified: getLastModified(c),
			HashInfo: utils.NewHashInfoByMap(h),
		},
		Reader:       c.Request.Body,
		Mimetype:     mimetype,
		WebPutAsTask: asTask,
	}
	var t task.TaskExtensionInfo
	if asTask {
		t, err = fs.PutAsTask(c.Request.Context(), dir, s)
	} else {
		err = fs.PutDirectly(c.Request.Context(), dir, s, true)
	}
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
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
	s := &stream.FileStream{
		Obj: &model.Object{
			Name:     name,
			Size:     file.Size,
			Modified: getLastModified(c),
			HashInfo: utils.NewHashInfoByMap(h),
		},
		Reader:       f,
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
		err = fs.PutDirectly(c.Request.Context(), dir, s, true)
	}
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if t == nil {
		common.SuccessResp(c)
		return
	}
	common.SuccessResp(c, gin.H{
		"task": getTaskInfo(t),
	})
}

// FsChunkUpload handles uploading a single chunk of a large file
func FsChunkUpload(c *gin.Context) {
	uploadId := c.Query("upload_id")
	indexStr := c.Query("index")
	if uploadId == "" || indexStr == "" {
		common.ErrorStrResp(c, "upload_id and index are required", 400)
		return
	}

	if _, err := strconv.Atoi(indexStr); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	// Get the chunk file from form
	file, err := c.FormFile("file")
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	// Create chunk directory
	chunkDir := stdpath.Join(conf.Conf.TempDir, "chunks", uploadId)
	if err := os.MkdirAll(chunkDir, 0755); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	// Save chunk to file
	chunkPath := stdpath.Join(chunkDir, indexStr)
	// Get CRC32 from header
	expectedCRC32 := c.GetHeader("X-Chunk-CRC32")

	// Save the uploaded file temporarily
	if err := c.SaveUploadedFile(file, chunkPath); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	// Always calculate CRC32 of the saved chunk for verification and response
	f, err := os.Open(chunkPath)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	defer f.Close()

	actualCRC32, err := utils.HashReader(utils.CRC32, f)
	if err != nil {
		os.Remove(chunkPath) // Clean up
		common.ErrorResp(c, err, 500)
		return
	}

	// Verify CRC32 if provided
	if expectedCRC32 != "" {
		if actualCRC32 != expectedCRC32 {
			os.Remove(chunkPath) // Clean up
			common.ErrorStrResp(c, fmt.Sprintf("chunk CRC32 mismatch: client=%s, server=%s", expectedCRC32, actualCRC32), 400)
			return
		}
	}

	common.SuccessResp(c, gin.H{
		"crc32": actualCRC32,
	})
}

// FsChunkMerge merges all chunks into a single file and uploads it
func FsChunkMerge(c *gin.Context) {
	var req struct {
		UploadId     string `json:"upload_id"`
		Path         string `json:"path"`
		TotalChunks  int    `json:"total_chunks"`
		AsTask       bool   `json:"as_task"`
		Overwrite    bool   `json:"overwrite"`
		LastModified int64  `json:"last_modified"`
		Hash         string `json:"hash"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	path, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}

	// Check if file exists when not overwriting
	if !req.Overwrite {
		if res, _ := fs.Get(c.Request.Context(), path, &fs.GetArgs{NoLog: true}); res != nil {
			common.ErrorStrResp(c, "file exists", 403)
			return
		}
	}

	chunkDir := stdpath.Join(conf.Conf.TempDir, "chunks", req.UploadId)

	// Check if all chunks exist (quick check, no heavy I/O)
	for i := 0; i < req.TotalChunks; i++ {
		chunkPath := stdpath.Join(chunkDir, strconv.Itoa(i))
		if _, err := os.Stat(chunkPath); os.IsNotExist(err) {
			common.ErrorStrResp(c, "chunk "+strconv.Itoa(i)+" not found", 400)
			return
		}
	}

	dir, name := stdpath.Split(path)

	// Check if system file should be ignored
	if shouldIgnoreSystemFile(name) {
		os.RemoveAll(chunkDir)
		common.ErrorStrResp(c, errs.IgnoredSystemFile.Error(), 403)
		return
	}

	lastModified := time.Now()
	if req.LastModified > 0 {
		lastModified = time.UnixMilli(req.LastModified)
	}

	// For as_task=true (large files), immediately return and process in background
	if req.AsTask {
		// Generate a simple task ID for tracking
		taskId := fmt.Sprintf("merge-%s", req.UploadId)

		// Start background goroutine for merge
		go func() {
			utils.Log.Infof("[ChunkMerge] Starting background merge for %s", path)

			// Create merged file
			mergedPath := stdpath.Join(chunkDir, "merged")
			mergedFile, err := os.Create(mergedPath)
			if err != nil {
				utils.Log.Errorf("[ChunkMerge] Failed to create merged file: %v", err)
				return
			}

			// Merge all chunks while computing hash
			var totalSize int64
			hasher := utils.NewMultiHasher([]*utils.HashType{utils.XXH64, utils.CRC64})
			multiWriter := io.MultiWriter(mergedFile, hasher)
			for i := 0; i < req.TotalChunks; i++ {
				chunkPath := stdpath.Join(chunkDir, strconv.Itoa(i))
				chunk, err := os.Open(chunkPath)
				if err != nil {
					mergedFile.Close()
					utils.Log.Errorf("[ChunkMerge] Failed to open chunk %d: %v", i, err)
					return
				}
				n, err := io.Copy(multiWriter, chunk)
				chunk.Close()
				if err != nil {
					mergedFile.Close()
					utils.Log.Errorf("[ChunkMerge] Failed to copy chunk %d: %v", i, err)
					return
				}
				totalSize += n
			}
			mergedFile.Close()

			hashInfo := hasher.GetHashInfo()
			hashMap := hashInfo.Export()

			// Verify client provided hash (xxHash64)
			if req.Hash != "" {
				for ht, hashValue := range hashMap {
					if ht.Name == "xxh64" && hashValue != req.Hash {
						os.RemoveAll(chunkDir)
						utils.Log.Errorf("[ChunkMerge] Hash mismatch: Client=%s, Server=%s", req.Hash, hashValue)
						return
					}
				}
			}

			utils.Log.Infof("[ChunkMerge] Merge complete. Size: %d bytes. Uploading to storage...", totalSize)

			// Open merged file for upload
			mergedReader, err := os.Open(mergedPath)
			if err != nil {
				utils.Log.Errorf("[ChunkMerge] Failed to open merged file: %v", err)
				return
			}

			s := &stream.FileStream{
				Obj: &model.Object{
					Name:     name,
					Size:     totalSize,
					Modified: lastModified,
				},
				Reader:       mergedReader,
				Mimetype:     utils.GetMimeType(name),
				WebPutAsTask: true,
			}
			s.Closers.Add(utils.CloseFunc(func() error {
				mergedReader.Close()
				os.RemoveAll(chunkDir)
				return nil
			}))

			// Use background context since original request context is gone
			ctx := context.Background()
			_, err = fs.PutAsTask(ctx, dir, s)
			if err != nil {
				utils.Log.Errorf("[ChunkMerge] Failed to put as task: %v", err)
				return
			}
			utils.Log.Infof("[ChunkMerge] Successfully queued upload task for %s", path)
		}()

		// Immediately return success with task info
		common.SuccessResp(c, gin.H{
			"task": gin.H{
				"id":      taskId,
				"status":  "processing",
				"message": "Merge started in background. Check Tasks page for progress.",
			},
		})
		return
	}

	// For as_task=false (small files or direct upload), use synchronous logic
	mergedPath := stdpath.Join(chunkDir, "merged")
	mergedFile, err := os.Create(mergedPath)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	// Merge all chunks while computing hash
	var totalSize int64
	hasher := utils.NewMultiHasher([]*utils.HashType{utils.XXH64, utils.CRC64})
	multiWriter := io.MultiWriter(mergedFile, hasher)
	for i := 0; i < req.TotalChunks; i++ {
		chunkPath := stdpath.Join(chunkDir, strconv.Itoa(i))
		chunk, err := os.Open(chunkPath)
		if err != nil {
			mergedFile.Close()
			common.ErrorResp(c, err, 500)
			return
		}
		n, err := io.Copy(multiWriter, chunk)
		chunk.Close()
		if err != nil {
			mergedFile.Close()
			common.ErrorResp(c, err, 500)
			return
		}
		totalSize += n
	}
	mergedFile.Close()
	hashInfo := hasher.GetHashInfo()
	hashMap := hashInfo.Export()
	// Prepare hash map for response
	hashResponse := make(map[string]string)
	for ht, hashValue := range hashMap {
		hashResponse[ht.Name] = hashValue
	}

	// Verify client provided hash (xxHash64)
	if req.Hash != "" {
		if serverHash, ok := hashResponse["xxh64"]; ok {
			if serverHash != req.Hash {
				// Hash mismatch!
				os.Remove(mergedPath)
				common.ErrorStrResp(c, fmt.Sprintf("Hash mismatch: Client=%s, Server=%s", req.Hash, serverHash), 400)
				return
			}
		}
	}

	// Open merged file for upload
	mergedReader, err := os.Open(mergedPath)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	s := &stream.FileStream{
		Obj: &model.Object{
			Name:     name,
			Size:     totalSize,
			Modified: lastModified,
		},
		Reader:       mergedReader,
		Mimetype:     utils.GetMimeType(name),
		WebPutAsTask: false,
	}
	s.Closers.Add(utils.CloseFunc(func() error {
		mergedReader.Close()
		os.RemoveAll(chunkDir)
		return nil
	}))

	err = fs.PutDirectly(c.Request.Context(), dir, s, true)
	mergedReader.Close()
	os.RemoveAll(chunkDir)

	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	common.SuccessResp(c, gin.H{
		"hash": hashResponse,
	})
}
 
