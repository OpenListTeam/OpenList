package fs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/model/reqres"
	"github.com/OpenListTeam/OpenList/v4/internal/model/tables"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// SliceUploadManager 分片上传管理器
type SliceUploadManager struct {
	sessionG singleflight.Group[*SliceUploadSession]
	cache    sync.Map // TaskID -> *SliceUploadSession
}

// SliceUploadSession 分片上传会话
type SliceUploadSession struct {
	*tables.SliceUpload
	tmpFile *os.File
	mutex   sync.Mutex
}

// CreateSession 创建新的上传会话
func (m *SliceUploadManager) CreateSession(ctx context.Context, storage driver.Driver, actualPath string, req *reqres.PreupReq) (*reqres.PreupResp, error) {
	srcobj, err := op.Get(ctx, storage, actualPath)
	if err != nil {
		log.Error(err)
		return nil, errors.WithStack(err)
	}
	user := ctx.Value(conf.UserKey).(*model.User)

	taskID := uuid.New().String()

	createsu := &tables.SliceUpload{
		TaskID:       taskID,
		DstPath:      req.Path,
		DstID:        srcobj.GetID(),
		Size:         req.Size,
		Name:         req.Name,
		HashMd5:      req.Hash.Md5,
		HashMd5256KB: req.Hash.Md5256KB,
		HashSha1:     req.Hash.Sha1,
		Overwrite:    req.Overwrite,
		ActualPath:   actualPath,
		AsTask:       req.AsTask,
		UserID:       user.ID,
	}
	log.Debugf("storage mount path %s", storage.GetStorage().MountPath)

	switch st := storage.(type) {
	case driver.IPreup:
		log.Info("preup support")
		res, err := st.Preup(ctx, srcobj, req)
		if err != nil {
			log.Error("Preup error", req, err)
			return nil, errors.WithStack(err)
		}
		log.Info("Preup success", res)
		if res.Reuse { //秒传
			return &reqres.PreupResp{
				Reuse:     true,
				SliceCnt:  0,
				SliceSize: res.SliceSize,
				TaskID:    taskID,
			}, nil
		}
		createsu.PreupID = res.PreupID
		createsu.SliceSize = res.SliceSize
		createsu.Server = res.Server
	default:
		log.Info("Preup not support")
		createsu.SliceSize = 10 * utils.MB
	}

	createsu.SliceCnt = uint((req.Size + createsu.SliceSize - 1) / createsu.SliceSize)
	createsu.SliceUploadStatus = make([]byte, (createsu.SliceCnt+7)/8)
	createsu.Status = tables.SliceUploadStatusWaiting // 设置初始状态

	err = db.CreateSliceUpload(createsu)
	if err != nil {
		log.Error("CreateSliceUpload error", createsu, err)
		return nil, errors.WithStack(err)
	}

	session := &SliceUploadSession{SliceUpload: createsu}
	m.cache.Store(taskID, session)

	return &reqres.PreupResp{
		Reuse:             false,
		SliceUploadStatus: createsu.SliceUploadStatus,
		SliceSize:         createsu.SliceSize,
		SliceCnt:          createsu.SliceCnt,
		TaskID:            createsu.TaskID,
	}, nil
}

func (m *SliceUploadManager) getOrLoadSession(taskID string) (*SliceUploadSession, error) {
	session, err, _ := m.sessionG.Do(taskID, func() (*SliceUploadSession, error) {
		if s, ok := m.cache.Load(taskID); ok {
			return s.(*SliceUploadSession), nil
		}
		su, err := db.GetSliceUploadByTaskID(taskID)
		if err != nil {
			return nil, errors.WithMessagef(err, "failed get slice upload [%s]", taskID)
		}
		s := &SliceUploadSession{
			SliceUpload: su,
		}
		m.cache.Store(taskID, s)
		return s, nil
	})
	return session, err
}

// UploadSlice 流式上传分片
func (m *SliceUploadManager) UploadSlice(ctx context.Context, storage driver.Driver, req *reqres.UploadSliceReq, reader io.Reader) error {
	session, err := m.getOrLoadSession(req.TaskID)
	if err != nil {
		log.Errorf("failed to get session: %+v", err)
		return err
	}

	defer func() {
		if err != nil {
			session.mutex.Lock()
			session.Status = tables.SliceUploadStatusFailed
			session.Message = err.Error()
			updateData := *session.SliceUpload
			session.mutex.Unlock()

			if updateErr := db.UpdateSliceUpload(&updateData); updateErr != nil {
				log.Errorf("Failed to update slice upload status: %v", updateErr)
			}
		}
	}()

	// 使用锁保护状态检查
	session.mutex.Lock()
	if tables.IsSliceUploaded(session.SliceUploadStatus, int(req.SliceNum)) {
		session.mutex.Unlock()
		log.Warnf("slice already uploaded,req:%+v", req)
		return nil
	}
	session.mutex.Unlock()

	// 分片hash验证逻辑
	if req.SliceHash != "" {
		session.mutex.Lock()

		if req.SliceNum == 0 {
			hs := strings.Split(req.SliceHash, ",")
			if len(hs) != int(session.SliceCnt) {
				session.mutex.Unlock()
				err := fmt.Errorf("slice hash count mismatch, expected %d, got %d", session.SliceCnt, len(hs))
				log.Error("slice hash count mismatch", req, err)
				return err
			}
			session.SliceHash = req.SliceHash
		} else {
			log.Debugf("Slice %d hash: %s (keeping complete hash list)", req.SliceNum, req.SliceHash)
		}
		session.mutex.Unlock()
	}

	switch s := storage.(type) {
	case driver.ISliceUpload:
		if err := s.SliceUpload(ctx, session.SliceUpload, req.SliceNum, reader); err != nil {
			log.Errorf("Native slice upload failed - TaskID: %s, SliceNum: %d, Error: %v",
				req.TaskID, req.SliceNum, err)
			return errors.WithMessagef(err, "slice %d upload failed", req.SliceNum)
		}
		log.Debugf("Native slice upload success - TaskID: %s, SliceNum: %d",
			req.TaskID, req.SliceNum)

	default:
		if err := session.ensureTmpFile(); err != nil {
			log.Error("ensureTmpFile error", req, err)
			return err
		}

		sw := &sliceWriter{
			file:   session.tmpFile,
			offset: int64(req.SliceNum) * int64(session.SliceSize),
		}
		_, err := utils.CopyWithBuffer(sw, reader)
		if err != nil {
			log.Error("Copy error", req, err)
			return err
		}
	}

	session.mutex.Lock()
	tables.SetSliceUploaded(session.SliceUploadStatus, int(req.SliceNum))
	updateData := *session.SliceUpload
	session.mutex.Unlock()

	err = db.UpdateSliceUpload(&updateData)
	if err != nil {
		log.Error("UpdateSliceUpload error", updateData, err)
		return err
	}
	return nil
}

// CompleteUpload 完成上传
func (m *SliceUploadManager) CompleteUpload(ctx context.Context, storage driver.Driver, taskID string) (*reqres.UploadSliceCompleteResp, error) {
	var err error

	session, err := m.getOrLoadSession(taskID)
	if err != nil {
		log.Errorf("failed to get session: %+v", err)
		return nil, err
	}

	// 检查是否所有分片都已上传
	session.mutex.Lock()
	allUploaded := tables.IsAllSliceUploaded(session.SliceUploadStatus, session.SliceCnt)
	session.mutex.Unlock()

	if !allUploaded {
		return &reqres.UploadSliceCompleteResp{
			Complete:          0,
			SliceUploadStatus: session.SliceUploadStatus,
			TaskID:            session.TaskID,
		}, nil
	}

	defer func() {
		session.cleanup()
		m.cache.Delete(session.TaskID)

		if err != nil {
			session.mutex.Lock()
			session.Status = tables.SliceUploadStatusFailed
			session.Message = err.Error()
			updateData := *session.SliceUpload
			session.mutex.Unlock()

			if updateErr := db.UpdateSliceUpload(&updateData); updateErr != nil {
				log.Errorf("Failed to update slice upload status: %v", updateErr)
			}
		} else {
			if deleteErr := db.DeleteSliceUploadByTaskID(session.TaskID); deleteErr != nil {
				log.Errorf("Failed to delete slice upload record: %v", deleteErr)
			}
		}
	}()

	switch s := storage.(type) {
	case driver.IUploadSliceComplete:
		err = s.UploadSliceComplete(ctx, session.SliceUpload)
		if err != nil {
			log.Error("UploadSliceComplete error", session.SliceUpload, err)
			return nil, err
		}

		return &reqres.UploadSliceCompleteResp{
			Complete: 1,
			TaskID:   session.TaskID,
		}, nil

	default:
		session.mutex.Lock()
		tmpFile := session.tmpFile
		session.mutex.Unlock()

		if tmpFile == nil {
			err := fmt.Errorf("tmp file not found [%s]", taskID)
			log.Error(err)
			return nil, err
		}

		var hashInfo utils.HashInfo
		if session.HashMd5 != "" {
			hashInfo = utils.NewHashInfo(utils.MD5, session.HashMd5)
		} else if session.HashSha1 != "" {
			hashInfo = utils.NewHashInfo(utils.SHA1, session.HashSha1)
		}

		file := &stream.FileStream{
			Obj: &model.Object{
				Name:     session.Name,
				Size:     session.Size,
				Modified: time.Now(),
				HashInfo: hashInfo,
			},
		}
		file.Mimetype = utils.GetMimeType(session.Name)

		if session.AsTask {
			file.SetTmpFile(tmpFile)
			session.mutex.Lock()
			session.tmpFile = nil
			session.TmpFile = ""
			session.mutex.Unlock()

			_, err = putAsTask(ctx, session.DstPath, file)
			if err != nil {
				log.Error("putAsTask error", session.SliceUpload, err)
				return nil, err
			}
			return &reqres.UploadSliceCompleteResp{
				Complete: 2,
				TaskID:   session.TaskID,
			}, nil
		}

		file.Reader = tmpFile
		err = op.Put(ctx, storage, session.ActualPath, file, nil)
		if err != nil {
			log.Error("Put error", session.SliceUpload, err)
			return nil, err
		}
		return &reqres.UploadSliceCompleteResp{
			Complete: 1,
			TaskID:   session.TaskID,
		}, nil
	}
}

func (s *SliceUploadSession) ensureTmpFile() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.TmpFile == "" {
		filename := fmt.Sprintf("slice_upload_%s_%s", s.TaskID, s.Name)
		filename = strings.ReplaceAll(filename, "/", "_")
		filename = strings.ReplaceAll(filename, "\\", "_")
		filename = strings.ReplaceAll(filename, ":", "_")

		tmpPath := filepath.Join(conf.GetPersistentTempDir(), filename)

		tf, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			return fmt.Errorf("create persistent temp file error: %w", err)
		}

		if err = os.Truncate(tmpPath, int64(s.Size)); err != nil {
			tf.Close()         // 确保文件被关闭
			os.Remove(tmpPath) // 清理文件
			return fmt.Errorf("truncate persistent temp file error: %w", err)
		}

		s.TmpFile = tmpPath
		s.tmpFile = tf

		// 更新数据库中的临时文件路径
		if updateErr := db.UpdateSliceUpload(s.SliceUpload); updateErr != nil {
			log.Errorf("Failed to update temp file path in database: %v", updateErr)
		}

		log.Debugf("Created persistent temp file: %s", tmpPath)
		return nil
	}

	if s.tmpFile == nil {
		var err error
		s.tmpFile, err = os.OpenFile(s.TmpFile, os.O_RDWR, 0644)
		if err != nil {
			return fmt.Errorf("reopen persistent temp file error: %w", err)
		}
		log.Debugf("Reopened persistent temp file: %s", s.TmpFile)
	}
	return nil
}

func (s *SliceUploadSession) cleanup() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.tmpFile != nil {
		if closeErr := s.tmpFile.Close(); closeErr != nil {
			log.Errorf("Failed to close tmp file: %v", closeErr)
		}
		s.tmpFile = nil
	}

	if s.TmpFile != "" {
		if removeErr := os.Remove(s.TmpFile); removeErr != nil && !os.IsNotExist(removeErr) {
			log.Errorf("Failed to remove tmp file %s: %v", s.TmpFile, removeErr)
		}
		s.TmpFile = ""
	}
}

// 全局管理器实例使用延迟初始化
var globalSliceManager *SliceUploadManager

func InitSliceUploadManager() {
	log.Info("Initializing slice upload manager...")
	globalSliceManager = &SliceUploadManager{}
	go globalSliceManager.cleanupIncompleteUploads()
}

// sliceWriter 分片写入器 - 保持原始实现
type sliceWriter struct {
	file   *os.File
	offset int64
}

// Write implements io.Writer interface
// 虽然每个分片都定义了一个sliceWriter
// 但是Write方法会在同一个分片复制过程中多次调用，
// 所以要更新自身的offset
func (sw *sliceWriter) Write(p []byte) (int, error) {
	n, err := sw.file.WriteAt(p, sw.offset)
	sw.offset += int64(n)
	return n, err
}

// cleanupIncompleteUploads 清理重启后未完成的上传任务和临时文件
func (m *SliceUploadManager) cleanupIncompleteUploads() {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("Panic in cleanupIncompleteUploads: %v", r)
		}
	}()

	time.Sleep(10 * time.Second)

	log.Info("Starting cleanup of incomplete slice uploads after restart...")

	incompleteUploads, err := db.GetIncompleteSliceUploads()
	if err != nil {
		log.Errorf("Failed to get incomplete slice uploads: %v", err)
	} else {
		if len(incompleteUploads) == 0 {
			log.Info("No incomplete slice uploads found in database")
		} else {
			log.Infof("Found %d incomplete slice uploads in database, starting cleanup...", len(incompleteUploads))
			cleanedCount := 0
			for _, upload := range incompleteUploads {
				if m.cleanupSingleUpload(upload) {
					cleanedCount++
				}
			}
			log.Infof("Database cleanup completed, cleaned up %d tasks", cleanedCount)
		}
	}

	m.cleanupOrphanedTempFiles()

	log.Info("Slice upload cleanup completed")
}

func (m *SliceUploadManager) cleanupSingleUpload(upload *tables.SliceUpload) bool {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("Panic in cleanupSingleUpload for task %s: %v", upload.TaskID, r)
		}
	}()

	log.Infof("Cleaning up upload task: %s, status: %s", upload.TaskID, upload.Status)

	if upload.TmpFile != "" {
		if err := os.Remove(upload.TmpFile); err != nil && !os.IsNotExist(err) {
			log.Warnf("Failed to remove temp file %s for task %s: %v", upload.TmpFile, upload.TaskID, err)
		} else {
			log.Debugf("Removed temp file: %s", upload.TmpFile)
		}
	}

	if err := db.DeleteSliceUploadByTaskID(upload.TaskID); err != nil {
		log.Errorf("Failed to delete slice upload task %s: %v", upload.TaskID, err)
		return false
	}

	log.Infof("Successfully cleaned up task: %s", upload.TaskID)
	return true
}

func (m *SliceUploadManager) cleanupOrphanedTempFiles() {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("Panic in cleanupOrphanedTempFiles: %v", r)
		}
	}()

	tempDir := conf.GetPersistentTempDir()
	if tempDir == "" {
		log.Warn("Persistent temp directory not configured, skipping orphaned file cleanup")
		return
	}

	log.Infof("Cleaning up orphaned temp files in: %s", tempDir)

	entries, err := os.ReadDir(tempDir)
	if err != nil {
		log.Errorf("Failed to read temp directory %s: %v", tempDir, err)
		return
	}

	orphanedCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		fileName := entry.Name()
		if !strings.HasPrefix(fileName, "slice_upload_") {
			continue
		}

		filePath := filepath.Join(tempDir, fileName)
		fileInfo, err := entry.Info()
		if err != nil {
			log.Warnf("Failed to get file info for %s: %v", filePath, err)
			continue
		}

		if time.Since(fileInfo.ModTime()) < 24*time.Hour {
			continue
		}

		if err := os.Remove(filePath); err != nil {
			log.Warnf("Failed to remove orphaned temp file %s: %v", filePath, err)
		} else {
			log.Debugf("Removed orphaned temp file: %s", filePath)
			orphanedCount++
		}
	}

	if orphanedCount > 0 {
		log.Infof("Cleaned up %d orphaned temp files", orphanedCount)
	} else {
		log.Info("No orphaned temp files found")
	}
}
