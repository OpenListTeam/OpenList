package db

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model/tables"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func CreateSliceUpload(su *tables.SliceUpload) error {
	return errors.WithStack(db.Create(su).Error)
}

func GetSliceUpload(wh map[string]any) (*tables.SliceUpload, error) {
	su := &tables.SliceUpload{}
	return su, db.Where(wh).First(su).Error
}

// GetSliceUploadByTaskID 通过TaskID获取分片上传记录
func GetSliceUploadByTaskID(taskID string) (*tables.SliceUpload, error) {
	su := &tables.SliceUpload{}
	return su, db.Where("task_id = ?", taskID).First(su).Error
}

func UpdateSliceUpload(su *tables.SliceUpload) error {
	return errors.WithStack(db.Save(su).Error)
}

// DeleteSliceUpload 删除分片上传记录
func DeleteSliceUpload(id uint) error {
	return errors.WithStack(db.Delete(&tables.SliceUpload{}, id).Error)
}

// DeleteSliceUploadByTaskID 通过TaskID删除分片上传记录
func DeleteSliceUploadByTaskID(taskID string) error {
	return errors.WithStack(db.Where("task_id = ?", taskID).Delete(&tables.SliceUpload{}).Error)
}

// GetIncompleteSliceUploads 获取所有未完成的分片上传任务（用于重启恢复）
func GetIncompleteSliceUploads() ([]*tables.SliceUpload, error) {
	var uploads []*tables.SliceUpload
	err := db.Where("status IN (?)", []int{
		tables.SliceUploadStatusWaiting,
		tables.SliceUploadStatusUploading,
		tables.SliceUploadStatusProxyComplete,
		tables.SliceUploadStatusPendingComplete,
	}).Find(&uploads).Error

	if err != nil {
		return nil, errors.WithStack(err)
	}

	return uploads, nil
}

// UpdateSliceUploadWithTx 使用事务更新分片上传状态，确保数据一致性
func UpdateSliceUploadWithTx(su *tables.SliceUpload) error {
	return errors.WithStack(db.Transaction(func(tx *gorm.DB) error {
		return tx.Save(su).Error
	}))
}

// UpdateSliceStatusAtomic 原子性地更新分片状态
func UpdateSliceStatusAtomic(taskID string, sliceNum int, status []byte) error {
	return errors.WithStack(db.Transaction(func(tx *gorm.DB) error {
		var su tables.SliceUpload
		if err := tx.Where("task_id = ?", taskID).First(&su).Error; err != nil {
			return err
		}

		tables.SetSliceUploaded(su.SliceUploadStatus, sliceNum)

		return tx.Save(&su).Error
	}))
}

// CleanupOrphanedSliceUploads 清理孤儿分片上传记录（启动时调用）
func CleanupOrphanedSliceUploads() error {
	cutoff := time.Now().Add(-24 * time.Hour)

	var orphanedTasks []tables.SliceUpload
	if err := db.Where("status IN (?, ?) AND updated_at < ?",
		tables.SliceUploadStatusWaiting,
		tables.SliceUploadStatusUploading,
		cutoff).Find(&orphanedTasks).Error; err != nil {
		return errors.WithStack(err)
	}

	cleanedCount := 0
	for _, task := range orphanedTasks {
		if task.TmpFile != "" {
			if err := os.Remove(task.TmpFile); err != nil && !os.IsNotExist(err) {
				log.Warnf("Failed to remove orphaned tmp file %s: %v", task.TmpFile, err)
			} else if err == nil {
				log.Debugf("Removed orphaned tmp file: %s", task.TmpFile)
			}
		}

		if err := db.Delete(&task).Error; err != nil {
			log.Errorf("Failed to delete orphaned slice upload task %s: %v", task.TaskID, err)
		} else {
			cleanedCount++
		}
	}

	if cleanedCount > 0 {
		log.Debugf("Cleaned up %d orphaned slice upload tasks", cleanedCount)
	}

	return cleanupOrphanedTempFiles()
}

// cleanupOrphanedTempFiles 清理临时目录中的孤儿文件
func cleanupOrphanedTempFiles() error {
	tempDir := conf.GetPersistentTempDir()

	if _, err := os.Stat(tempDir); os.IsNotExist(err) {
		log.Debugf("Temp directory does not exist: %s", tempDir)
		return nil
	}

	var activeTasks []tables.SliceUpload
	if err := db.Where("tmp_file IS NOT NULL AND tmp_file != '' AND status IN (?, ?)",
		tables.SliceUploadStatusWaiting,
		tables.SliceUploadStatusUploading).Find(&activeTasks).Error; err != nil {
		return errors.WithStack(err)
	}

	activeFiles := make(map[string]bool)
	for _, task := range activeTasks {
		if task.TmpFile != "" {
			activeFiles[task.TmpFile] = true
		}
	}

	cleanedCount := 0
	cutoff := time.Now().Add(-24 * time.Hour)

	err := filepath.WalkDir(tempDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Warnf("Failed to access path %s: %v", path, err)
			return nil // 继续处理其他文件
		}

		if d.IsDir() {
			return nil
		}

		if !strings.HasPrefix(d.Name(), "slice_upload_") {
			return nil
		}

		if activeFiles[path] {
			return nil // 文件仍在使用中，跳过
		}

		info, err := d.Info()
		if err != nil {
			log.Warnf("Failed to get file info for %s: %v", path, err)
			return nil
		}

		if info.ModTime().After(cutoff) {
			return nil
		}

		if err := os.Remove(path); err != nil {
			log.Warnf("Failed to remove orphaned temp file %s: %v", path, err)
		} else {
			log.Debugf("Removed orphaned temp file: %s", path)
			cleanedCount++
		}

		return nil
	})

	if err != nil {
		log.Errorf("Failed to walk temp directory %s: %v", tempDir, err)
		return errors.WithStack(err)
	}

	if cleanedCount > 0 {
		log.Debugf("Cleaned up %d orphaned temp files from %s", cleanedCount, tempDir)
	}

	return nil
}
