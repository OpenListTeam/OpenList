package _123_pan

import (
	"context"
	"fmt"
	"strconv"

	_123 "github.com/OpenListTeam/OpenList/v4/drivers/123"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
)

type Pan123 struct{}

func (*Pan123) Name() string {
	return "123Pan"
}

func (*Pan123) Items() []model.SettingItem {
	return []model.SettingItem{
		{Key: conf.Pan123TempDir, Value: "", Type: conf.TypeString, Group: model.OFFLINE_DOWNLOAD, Flag: model.PRIVATE},
	}
}

func (*Pan123) Run(_ *tool.DownloadTask) error {
	return errs.NotSupport
}

func (*Pan123) Init() (string, error) {
	return "ok", nil
}

func (*Pan123) IsReady() bool {
	tempDir := setting.GetStr(conf.Pan123TempDir)
	if tempDir == "" {
		return false
	}
	storage, _, err := op.GetStorageAndActualPath(tempDir)
	if err != nil {
		return false
	}
	if _, ok := storage.(*_123.Pan123); !ok {
		return false
	}
	return true
}

func (*Pan123) AddURL(args *tool.AddUrlArgs) (string, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(args.TempDir)
	if err != nil {
		return "", err
	}
	driver123, ok := storage.(*_123.Pan123)
	if !ok {
		return "", fmt.Errorf("unsupported storage driver for offline download, only 123Pan is supported")
	}
	ctx := context.Background()
	if err := op.MakeDir(ctx, storage, actualPath); err != nil {
		return "", err
	}
	parentDir, err := op.GetUnwrap(ctx, storage, actualPath)
	if err != nil {
		return "", err
	}
	taskID, err := driver123.OfflineDownload(ctx, args.Url, parentDir)
	if err != nil {
		return "", fmt.Errorf("failed to add offline download task: %w", err)
	}
	return strconv.FormatInt(taskID, 10), nil
}

func (*Pan123) Remove(task *tool.DownloadTask) error {
	taskID, err := strconv.ParseInt(task.GID, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse task ID: %s", task.GID)
	}
	storage, _, err := op.GetStorageAndActualPath(task.TempDir)
	if err != nil {
		return err
	}
	driver123, ok := storage.(*_123.Pan123)
	if !ok {
		return fmt.Errorf("unsupported storage driver for offline download, only 123Pan is supported")
	}
	return driver123.DeleteOfflineTasks(context.Background(), []int64{taskID})
}

func (*Pan123) Status(task *tool.DownloadTask) (*tool.Status, error) {
	taskID, err := strconv.ParseInt(task.GID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse task ID: %s", task.GID)
	}
	storage, _, err := op.GetStorageAndActualPath(task.TempDir)
	if err != nil {
		return nil, err
	}
	driver123, ok := storage.(*_123.Pan123)
	if !ok {
		return nil, fmt.Errorf("unsupported storage driver for offline download, only 123Pan is supported")
	}

	t, err := driver123.GetOfflineTask(context.Background(), taskID)
	if err != nil {
		return nil, err
	}

	var statusStr string
	completed := false
	var taskErr error
	switch t.Status {
	case 0:
		statusStr = "downloading"
	case 2:
		statusStr = "succeed"
		completed = true
	case 1:
		statusStr = "failed"
		taskErr = fmt.Errorf("offline download failed")
	case 3:
		statusStr = "retrying"
	default:
		statusStr = fmt.Sprintf("status_%d", t.Status)
	}

	return &tool.Status{
		TotalBytes: t.Size,
		Progress:   t.Progress,
		Completed:  completed,
		Status:     statusStr,
		Err:        taskErr,
	}, nil
}

var _ tool.Tool = (*Pan123)(nil)

func init() {
	tool.Tools.Add(&Pan123{})
}
