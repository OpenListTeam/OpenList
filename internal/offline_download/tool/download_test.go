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
	signalOnAdd  bool
	onStatus     func()
	name         string
}

func (t *cancelTestTool) Name() string {
	if t.name != "" {
		return t.name
	}
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

func (t *cancelTestTool) AddURL(args *AddUrlArgs) (string, error) {
	if t.signalOnAdd {
		go func() {
			select {
			case args.Signal <- 1:
			case <-args.Ctx.Done():
			}
		}()
	}
	return "provider-task-id", nil
}

func (t *cancelTestTool) Remove(*DownloadTask) error {
	t.removeCalled = true
	return nil
}

func (t *cancelTestTool) Status(*DownloadTask) (*Status, error) {
	if t.onStatus != nil {
		t.onStatus()
	}
	return &Status{Completed: true}, nil
}

func TestDownloadTaskRunPrioritizesCancellationAfterCompletionUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cleanup := &cancelTestTool{
		signalOnAdd: true,
		onStatus:    cancel,
		name:        "Thunder",
	}
	download := &DownloadTask{
		DstDirPath: "/dst",
		TempDir:    "/dst",
		Url:        "https://example.com/file",
		tool:       cleanup,
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

func TestDownloadTaskUpdateStopsBeforeTransferWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cleanup := &cancelTestTool{onStatus: cancel}
	download := &DownloadTask{
		DstDirPath:   "/missing-destination",
		TempDir:      "/provider-task",
		DeletePolicy: UploadDownloadStream,
		Url:          "https://example.com/file",
		tool:         cleanup,
	}
	download.SetCtx(ctx)

	ok, err := download.Update()
	if !ok {
		t.Fatal("DownloadTask.Update() did not report completion")
	}
	if !stderrors.Is(err, context.Canceled) {
		t.Fatalf("DownloadTask.Update() error = %v, want context.Canceled", err)
	}
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
