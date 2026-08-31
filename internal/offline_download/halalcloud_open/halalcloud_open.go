package halalcloudopen

import (
	"context"
	"errors"
	"fmt"

	halalcloudopendriver "github.com/OpenListTeam/OpenList/v4/drivers/halalcloud_open"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// HalalCloudOpen adapts the storage driver's native offline-task API to the
// generic OpenList download-task lifecycle.
type HalalCloudOpen struct{}

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
	// HalalCloudOpen is a destination-bound tool without a global temporary directory.
	// NamesForPath exposes it only when the destination uses the matching storage driver.
	return false
}

func (h *HalalCloudOpen) AddURL(args *tool.AddUrlArgs) (string, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(args.TempDir)
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
	// Resolve the OpenList path to the provider object before creating the task:
	// the API expects its native absolute path, not the storage mount path.
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
	storage, actualPath, err := op.GetStorageAndActualPath(task.TempDir)
	if err != nil {
		return err
	}
	driver, ok := storage.(*halalcloudopendriver.HalalCloudOpen)
	if !ok {
		return errors.New("HalalCloudOpen offline download only supports HalalCloudOpen destination storage")
	}

	// A canceled task may have written partial files outside OpenList's normal
	// mutation path. Invalidate both caches even when provider-side cleanup fails.
	defer op.Cache.DeleteDirectory(storage, actualPath)
	defer h.invalidateTaskCache(driver)
	// The task context is already canceled when Remove is called, so use an
	// independent context for the provider-side cleanup. Delete the task record
	// without deleting files that may already have reached the destination.
	if err := driver.DeleteOfflineTasks(context.Background(), []string{task.GID}, false); err != nil {
		return err
	}
	return nil
}

func (h *HalalCloudOpen) Status(task *tool.DownloadTask) (*tool.Status, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(task.TempDir)
	if err != nil {
		return nil, err
	}
	driver, ok := storage.(*halalcloudopendriver.HalalCloudOpen)
	if !ok {
		return nil, errors.New("HalalCloudOpen offline download only supports HalalCloudOpen destination storage")
	}

	tasks, err := h.getTasks(driver)
	if err != nil {
		return nil, err
	}
	for _, providerTask := range tasks {
		if providerTask != nil && providerTask.Identity == task.GID {
			status := statusFromTask(providerTask)
			if status.Completed || status.Err != nil {
				// HalalCloud writes directly to the destination, bypassing op's
				// normal cache invalidation. Refresh the directory after any
				// terminal state so completed or partial files become visible.
				op.Cache.DeleteDirectory(storage, actualPath)
			}
			return status, nil
		}
	}
	// A newly added provider task may not be visible immediately. Do not retain
	// a list that missed it; OpenList will retry Status and fetch a fresh page.
	// Also drop the destination listing in case the provider removed a terminal
	// task before OpenList observed its final state.
	h.invalidateTaskCache(driver)
	op.Cache.DeleteDirectory(storage, actualPath)
	return nil, fmt.Errorf("HalalCloudOpen offline task %s not found", task.GID)
}

var _ tool.Tool = (*HalalCloudOpen)(nil)

func init() {
	tool.Tools.Add(&HalalCloudOpen{})
}
