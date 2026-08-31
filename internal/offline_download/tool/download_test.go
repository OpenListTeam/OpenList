package tool

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type cancelTestTool struct {
	removeCalled bool
}

func (*cancelTestTool) Name() string {
	return "cancel-test"
}

func (*cancelTestTool) Items() []model.SettingItem {
	return nil
}

func (*cancelTestTool) Init() (string, error) {
	return "ok", nil
}

func (*cancelTestTool) IsReady() bool {
	return true
}

func (*cancelTestTool) Run(*DownloadTask) error {
	return errs.NotSupport
}

func (*cancelTestTool) AddURL(*AddUrlArgs) (string, error) {
	return "provider-task-id", nil
}

func (t *cancelTestTool) Remove(*DownloadTask) error {
	t.removeCalled = true
	return nil
}

func (*cancelTestTool) Status(*DownloadTask) (*Status, error) {
	return nil, nil
}

func TestDownloadTaskRunReturnsCanceledAfterCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cleanup := &cancelTestTool{}
	download := &DownloadTask{
		Url:  "https://example.com/file",
		tool: cleanup,
	}
	download.SetCtx(ctx)

	err := download.Run()
	if !stderrors.Is(err, context.Canceled) {
		t.Fatalf("DownloadTask.Run() error = %v, want context.Canceled", err)
	}
	if !cleanup.removeCalled {
		t.Fatal("DownloadTask.Run() did not clean up the provider task")
	}
	if download.Status != "offline download canceled" {
		t.Fatalf("DownloadTask status after cancellation = %q, want cancellation status", download.Status)
	}
}
