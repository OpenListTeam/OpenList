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
	removeCalled bool
	removeCalls  int
	removeErr    error
	signalOnAdd  bool
	onAddURL     func(*AddUrlArgs) (string, error)
	onRemove     func()
	onRun        func(*DownloadTask) error
	onStatus     func()
	name         string
}

type observedDownloadTask struct {
	*DownloadTask
	failedHookCalled bool
	canceled         chan struct{}
}

func (t *observedDownloadTask) SetState(state tache.State) {
	t.DownloadTask.SetState(state)
	if state == tache.StateCanceled && t.failedHookCalled {
		close(t.canceled)
	}
}

func (t *observedDownloadTask) OnFailed() {
	t.failedHookCalled = true
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

func (t *cancelTestTool) Remove(*DownloadTask) error {
	t.removeCalled = true
	t.removeCalls++
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
			download := &DownloadTask{tool: cleanup}
			download.SetCtx(ctx)

			started := time.Now()
			err := download.waitAndRemove(time.Hour)
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("waitAndRemove() took %s after cancellation", elapsed)
			}
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
	download := &DownloadTask{tool: cleanup}
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
	download := &DownloadTask{tool: cleanup}
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

func TestDownloadTaskManagerKeepsCanceledStateWhenCleanupFails(t *testing.T) {
	added := make(chan struct{})
	cleanupErr := stderrors.New("cleanup failed")
	provider := &cancelTestTool{
		removeErr: cleanupErr,
		onAddURL: func(*AddUrlArgs) (string, error) {
			close(added)
			return "provider-task-id", nil
		},
	}
	download := &observedDownloadTask{
		DownloadTask: &DownloadTask{tool: provider},
		canceled:     make(chan struct{}),
	}
	download.MaxRetry = 3
	manager := tache.NewManager[*observedDownloadTask](
		tache.WithWorks(1),
		tache.WithMaxRetry(3),
	)
	manager.Add(download)

	select {
	case <-added:
	case <-time.After(time.Second):
		t.Fatal("DownloadTask did not reach AddURL")
	}
	manager.Cancel(download.GetID())
	select {
	case <-download.canceled:
	case <-time.After(time.Second):
		t.Fatal("DownloadTask did not reach StateCanceled")
	}

	if download.GetState() != tache.StateCanceled {
		t.Fatalf("DownloadTask state = %v, want %v", download.GetState(), tache.StateCanceled)
	}
	if !stderrors.Is(download.GetErr(), context.Canceled) {
		t.Fatalf("DownloadTask error = %v, want context.Canceled", download.GetErr())
	}
	if !stderrors.Is(download.GetErr(), cleanupErr) {
		t.Fatalf("DownloadTask error = %v, want cleanup error", download.GetErr())
	}
	if retry, _ := download.GetRetry(); retry != 0 {
		t.Fatalf("DownloadTask retry count = %d, want 0", retry)
	}
	if provider.removeCalls != 1 {
		t.Fatalf("Remove() calls = %d, want 1", provider.removeCalls)
	}
}
