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
	storage, _, err := op.GetStorageAndActualPath(task.TempDir)
	if err != nil {
		return err
	}
	driver, ok := storage.(*halalcloudopendriver.HalalCloudOpen)
	if !ok {
		return errors.New("HalalCloudOpen offline download only supports HalalCloudOpen destination storage")
	}

	// Cancel the provider task record without deleting files that may already
	// have been written to the destination directory.
	if err := driver.DeleteOfflineTasks(context.Background(), []string{task.GID}, false); err != nil {
		return err
	}
	h.invalidateTaskCache(driver)
	return nil
}

func (h *HalalCloudOpen) Status(task *tool.DownloadTask) (*tool.Status, error) {
	storage, _, err := op.GetStorageAndActualPath(task.TempDir)
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
			return statusFromTask(providerTask), nil
		}
	}
	// A newly added provider task may not be visible immediately. Do not retain
	// a list that missed it; OpenList will retry Status and fetch a fresh page.
	h.invalidateTaskCache(driver)
	return nil, fmt.Errorf("HalalCloudOpen offline task %s not found", task.GID)
}

var _ tool.Tool = (*HalalCloudOpen)(nil)

func init() {
	tool.Tools.Add(&HalalCloudOpen{})
}
