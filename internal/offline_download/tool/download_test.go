package tool

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/tache"
)

type cancelTestTool struct {
	removeCalled   bool
	removeCalls    int
	removeErr      error
	signalOnAdd    bool
	onAddURL       func(*AddUrlArgs) (string, error)
	onRemove       func()
	removeCtx      context.Context
	removeCtxErr   error
	removeDeadline time.Time
	onRun          func(*DownloadTask) error
	onStatus       func()
	name           string
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

func (t *cancelTestTool) Run(task *DownloadTask) error {
	if t.onRun != nil {
		return t.onRun(task)
	}
	return errs.NotSupport
}

func (t *cancelTestTool) AddURL(args *AddUrlArgs) (string, error) {
	if t.onAddURL != nil {
		return t.onAddURL(args)
	}
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

func (t *cancelTestTool) Remove(ctx context.Context, _ *DownloadTask) error {
	t.removeCalled = true
	t.removeCalls++
	t.removeCtx = ctx
	t.removeCtxErr = ctx.Err()
	t.removeDeadline, _ = ctx.Deadline()
	if t.onRemove != nil {
		t.onRemove()
	}
	return t.removeErr
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

func TestDownloadTaskRunSkipsProviderWhenAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cleanup := &cancelTestTool{onRun: func(*DownloadTask) error {
		t.Fatal("provider Run called after cancellation")
		return nil
	}}
	download := &DownloadTask{
		Url:  "https://example.com/file",
		tool: cleanup,
	}
	download.SetCtx(ctx)

	err := download.Run()
	if !stderrors.Is(err, context.Canceled) {
		t.Fatalf("DownloadTask.Run() error = %v, want context.Canceled", err)
	}
	if cleanup.removeCalled {
		t.Fatal("DownloadTask.Run() tried to clean up a provider task that was never created")
	}
	if download.Status != "offline download canceled" {
		t.Fatalf("DownloadTask status after cancellation = %q, want cancellation status", download.Status)
	}
}

func TestDownloadTaskWorkerKeepsCanceledState(t *testing.T) {
	for _, cleanupErr := range []error{nil, stderrors.New("cleanup failed")} {
		ctx, cancel := context.WithCancel(context.Background())
		provider := &cancelTestTool{removeErr: cleanupErr, onAddURL: func(*AddUrlArgs) (string, error) {
			cancel()
			return "provider-task-id", nil
		}}
		download := &DownloadTask{tool: provider}
		download.SetCtx(ctx)
		download.MaxRetry = 3
		// Execute synchronously: exercise the real tache state machine without
		// manager goroutines, timing assumptions or hooks counting SetState calls.
		tache.Worker[*DownloadTask]{}.Execute(download)
		if download.GetState() != tache.StateCanceled || !stderrors.Is(download.GetErr(), context.Canceled) {
			t.Fatalf("state = %v, error = %v", download.GetState(), download.GetErr())
		}
		if cleanupErr != nil && !stderrors.Is(download.GetErr(), cleanupErr) {
			t.Fatal(download.GetErr())
		}
		if retries, _ := download.GetRetry(); retries != 0 {
			t.Fatalf("retries = %d", retries)
		}
		if provider.removeCalls != 1 {
			t.Fatalf("cleanup calls = %d", provider.removeCalls)
		}
	}
}

func TestDownloadTaskRunStopsDirectTransferWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	direct := &cancelTestTool{
		name: "SimpleHttp",
		onRun: func(*DownloadTask) error {
			cancel()
			return nil
		},
	}
	download := &DownloadTask{
		DstDirPath:   "/missing-destination",
		DeletePolicy: UploadDownloadStream,
		Url:          "https://example.com/file",
		tool:         direct,
	}
	download.SetCtx(ctx)

	err := download.Run()
	if !stderrors.Is(err, context.Canceled) {
		t.Fatalf("DownloadTask.Run() error = %v, want context.Canceled", err)
	}
	if direct.removeCalled {
		t.Fatal("DownloadTask.Run() tried provider cleanup for a direct download")
	}
}

func TestDownloadTaskWaitAndRemoveStopsOnCancellation(t *testing.T) {
	for _, toolName := range []string{"115 Cloud", "qBittorrent", "Transmission"} {
		t.Run(toolName, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			cleanup := &cancelTestTool{name: toolName}
			download := &DownloadTask{GID: "provider-task-id", tool: cleanup}
			download.SetCtx(ctx)

			err := download.waitAndRemove(time.Hour)
			if !stderrors.Is(err, context.Canceled) {
				t.Fatalf("waitAndRemove() error = %v, want context.Canceled", err)
			}
			if cleanup.removeCalls != 1 {
				t.Fatalf("Remove() calls = %d, want 1", cleanup.removeCalls)
			}
		})
	}
}

func TestDownloadTaskWaitAndRemoveNoticesCancellationDuringCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cleanup := &cancelTestTool{onRemove: cancel}
	download := &DownloadTask{GID: "provider-task-id", tool: cleanup}
	download.SetCtx(ctx)

	err := download.waitAndRemove(0)
	if !stderrors.Is(err, context.Canceled) {
		t.Fatalf("waitAndRemove() error = %v, want context.Canceled", err)
	}
	if cleanup.removeCalls != 1 {
		t.Fatalf("Remove() calls = %d, want 1", cleanup.removeCalls)
	}
}

func TestDownloadTaskCancelPreservesCleanupError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cleanupErr := stderrors.New("cleanup failed")
	cleanup := &cancelTestTool{removeErr: cleanupErr}
	download := &DownloadTask{GID: "provider-task-id", tool: cleanup}
	download.SetCtx(ctx)

	err := download.cancelDownload()
	if !stderrors.Is(err, context.Canceled) {
		t.Fatalf("cancelDownload() error = %v, want context.Canceled", err)
	}
	if !stderrors.Is(err, cleanupErr) {
		t.Fatalf("cancelDownload() error = %v, want cleanup error", err)
	}
	if cleanup.removeCalls != 1 {
		t.Fatalf("Remove() calls = %d, want 1", cleanup.removeCalls)
	}
}

func TestDownloadTaskCleanupUsesDetachedBoundedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cleanup := &cancelTestTool{}
	download := &DownloadTask{GID: "provider-task-id", tool: cleanup}
	download.SetCtx(ctx)

	if err := download.cancelDownload(); !stderrors.Is(err, context.Canceled) {
		t.Fatalf("cancelDownload() error = %v, want context.Canceled", err)
	}
	if cleanup.removeCtx == nil {
		t.Fatal("cancelDownload() did not pass a cleanup context")
	}
	if cleanup.removeCtxErr != nil {
		t.Fatalf("cleanup context is canceled: %v", cleanup.removeCtxErr)
	}
	if deadline := cleanup.removeDeadline; deadline.IsZero() || time.Until(deadline) <= 0 || time.Until(deadline) > cancellationCleanupTimeout {
		t.Fatalf("cleanup context deadline = %v, want within %s", deadline, cancellationCleanupTimeout)
	}
}

func TestDownloadTaskRunPreservesCancellationWhenAddURLFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	addErr := stderrors.New("add response lost")
	provider := &cancelTestTool{
		onAddURL: func(*AddUrlArgs) (string, error) {
			cancel()
			return "", addErr
		},
	}
	download := &DownloadTask{tool: provider}
	download.SetCtx(ctx)

	err := download.Run()
	if !stderrors.Is(err, context.Canceled) {
		t.Fatalf("DownloadTask.Run() error = %v, want context.Canceled", err)
	}
	if !stderrors.Is(err, addErr) {
		t.Fatalf("DownloadTask.Run() error = %v, want AddURL error", err)
	}
	if provider.removeCalled {
		t.Fatal("DownloadTask.Run() tried cleanup without a provider task ID")
	}
	if download.Status != "offline download canceled" {
		t.Fatalf("DownloadTask status after cancellation = %q, want cancellation status", download.Status)
	}
}
