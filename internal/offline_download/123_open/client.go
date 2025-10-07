package _123_open

import (
	"context"
	"fmt"
	"strconv"

	_123_open "github.com/OpenListTeam/OpenList/v4/drivers/123_open"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
)

type Open123 struct{}

func (*Open123) Name() string {
	return "123 Open"
}

func (*Open123) Items() []model.SettingItem {
	return nil
}

func (*Open123) Run(_ *tool.DownloadTask) error {
	return errs.NotSupport
}

func (*Open123) Init() (string, error) {
	return "ok", nil
}

func (*Open123) IsReady() bool {
	tempDir := setting.GetStr(conf.Pan123OpenTempDir)
	if tempDir == "" {
		return false
	}
	storage, _, err := op.GetStorageAndActualPath(tempDir)
	if err != nil {
		return false
	}
	if _, ok := storage.(*_123_open.Open123); !ok {
		return false
	}
	return true
}

func (*Open123) AddURL(args *tool.AddUrlArgs) (string, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(args.TempDir)
	if err != nil {
		return "", err
	}
	driver123Open, ok := storage.(*_123_open.Open123)
	if !ok {
		return "", fmt.Errorf("unsupported storage driver for offline download, only 123 Open is supported")
	}
	ctx := context.Background()
	if err := op.MakeDir(ctx, storage, actualPath); err != nil {
		return "", err
	}
	parentDir, err := op.GetUnwrap(ctx, storage, actualPath)
	if err != nil {
		return "", err
	}
	cb := setting.GetStr(conf.Pan123OpenOfflineDownloadCallbackUrl)
	taskID, err := driver123Open.OfflineDownload(ctx, args.Url, parentDir, cb)
	if err != nil {
		return "", fmt.Errorf("failed to add offline download task: %w", err)
	}
	return strconv.Itoa(taskID), nil
}

func (*Open123) Remove(_ *tool.DownloadTask) error {
	return errs.NotSupport
}

func (*Open123) Status(task *tool.DownloadTask) (*tool.Status, error) {
	taskID, err := strconv.Atoi(task.GID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse task ID: %s", task.GID)
	}
	storage, _, err := op.GetStorageAndActualPath(task.TempDir)
	if err != nil {
		return nil, err
	}
	driver123Open, ok := storage.(*_123_open.Open123)
	if !ok {
		return nil, fmt.Errorf("unsupported storage driver for offline download, only 123 Open is supported")
	}
	process, status, err := driver123Open.OfflineDownloadProcess(context.Background(), taskID)
	if err != nil {
		return nil, err
	}
	var statusStr string
	switch status {
	case 0:
		statusStr = "downloading"
	case 1:
		err = fmt.Errorf("offline download failed")
	case 2:
		statusStr = "succeed"
	case 3:
		statusStr = "retrying"
	}
	return &tool.Status{
		Progress:  process,
		Completed: status == 2,
		Status:    statusStr,
		Err:       err,
	}, nil
}

var _ tool.Tool = (*Open123)(nil)

func init() {
	tool.Tools.Add(&Open123{})
}
