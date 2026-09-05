package halalcloudopen

import (
	"context"
	"errors"
	"fmt"
	"time"

	halalcloudopendriver "github.com/OpenListTeam/OpenList/v4/drivers/halalcloud_open"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// HalalCloudOpen adapts the storage driver's native offline-task API to the
// generic OpenList download-task lifecycle.
type HalalCloudOpen struct{}

const halalCloudOfflineCleanupTimeout = 15 * time.Second

func (*HalalCloudOpen) Name() string {
	return "HalalCloudOpen"
}

func (*HalalCloudOpen) Items() []model.SettingItem {
	return nil
}

func (*HalalCloudOpen) Run(_ *tool.DownloadTask) error {
	return errs.NotSupport
}

func (*HalalCloudOpen) Init() (string, error) {
	return "ok", nil
}

func (*HalalCloudOpen) IsReady() bool {
	// Availability is resolved from the destination storage by NamesForPath.
	return false
}

func (h *HalalCloudOpen) AddURL(args *tool.AddUrlArgs) (string, error) {
	storage, actualPath, err := storageAndActualPath(args.TempDir, args.StorageMountPath)
	if err != nil {
		return "", err
	}
	driver, ok := storage.(*halalcloudopendriver.HalalCloudOpen)
	if !ok {
		return "", errors.New("HalalCloudOpen offline download only supports HalalCloudOpen destination storage")
	}

	if err := op.MakeDir(args.Ctx, storage, actualPath); err != nil {
		return "", err
	}
	// The provider object carries the native destination path expected by the API.
	parentDir, err := op.GetUnwrap(args.Ctx, storage, actualPath)
	if err != nil {
		return "", err
	}

	task, err := driver.OfflineDownload(args.Ctx, args.Url, parentDir)
	if err != nil {
		return "", fmt.Errorf("failed to add HalalCloudOpen offline download task: %w", err)
	}
	h.invalidateTaskCache(driver)
	return task.Identity, nil
}

func (h *HalalCloudOpen) Remove(task *tool.DownloadTask) error {
	storage, actualPath, err := storageAndActualPath(task.TempDir, task.StorageMountPath)
	if err != nil {
		return err
	}
	driver, ok := storage.(*halalcloudopendriver.HalalCloudOpen)
	if !ok {
		return errors.New("HalalCloudOpen offline download only supports HalalCloudOpen destination storage")
	}

	// Provider-side writes can leave cached task and directory-tree data stale.
	defer invalidateDestinationCache(storage, actualPath)
	defer h.invalidateTaskCache(driver)
	// Cleanup runs independently after the download task context is canceled,
	// while the timeout keeps a stalled provider from holding the worker.
	cleanupCtx, cancel := context.WithTimeout(detachedContext(task.Ctx()), halalCloudOfflineCleanupTimeout)
	defer cancel()
	if err := driver.DeleteOfflineTasks(cleanupCtx, []string{task.GID}, false); err != nil {
		return err
	}
	return nil
}

func (h *HalalCloudOpen) Status(task *tool.DownloadTask) (*tool.Status, error) {
	storage, actualPath, err := storageAndActualPath(task.TempDir, task.StorageMountPath)
	if err != nil {
		return nil, err
	}
	driver, ok := storage.(*halalcloudopendriver.HalalCloudOpen)
	if !ok {
		return nil, errors.New("HalalCloudOpen offline download only supports HalalCloudOpen destination storage")
	}

	tasks, err := h.getTasks(task.Ctx(), driver)
	if err != nil {
		return nil, err
	}
	for _, providerTask := range tasks {
		if providerTask != nil && providerTask.Identity == task.GID {
			status := statusFromTask(providerTask)
			if status.Completed || status.Err != nil {
				// Terminal provider writes invalidate the destination tree.
				invalidateDestinationCache(storage, actualPath)
			}
			return status, nil
		}
	}
	// Refresh provider and destination data before retrying a missing task.
	h.invalidateTaskCache(driver)
	invalidateDestinationCache(storage, actualPath)
	return nil, fmt.Errorf("HalalCloudOpen offline task %s not found", task.GID)
}

func invalidateDestinationCache(storage driver.Driver, actualPath string) {
	op.Cache.DeleteDirectoryTree(storage, actualPath)
}

func storageAndActualPath(rawPath, mountPath string) (driver.Driver, string, error) {
	if mountPath != "" {
		return op.GetStorageAndActualPathByMountPath(rawPath, mountPath)
	}
	return op.GetStorageAndActualPath(rawPath)
}

func detachedContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

var _ tool.Tool = (*HalalCloudOpen)(nil)

func init() {
	tool.Tools.Add(&HalalCloudOpen{})
}
