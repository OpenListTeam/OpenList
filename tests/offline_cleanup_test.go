package tests

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/bootstrap"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
)

type cleanupStore struct {
	data []byte
}

func (s *cleanupStore) read() ([]byte, error) {
	if len(s.data) == 0 {
		return []byte("[]"), nil
	}
	return append([]byte(nil), s.data...), nil
}

func (s *cleanupStore) write(data []byte) error {
	s.data = append(s.data[:0], data...)
	return nil
}

func TestOfflineCleanupBlocksUntilFailedTransferSucceeds(t *testing.T) {
	store := &cleanupStore{}
	executed := 0
	manager, err := tool.NewCleanupManager(store.read, store.write, func(context.Context, tool.CleanupJob) error {
		executed++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(-time.Minute)
	job := tool.CleanupJob{
		ID:       "cleanup-1",
		TempDir:  "/tmp/openlist/qBittorrent/task-1",
		Toolname: "qBittorrent",
	}
	if err := manager.Register(job); err != nil {
		t.Fatal(err)
	}
	if err := manager.BeginTransfer(job.ID, "gid-1", deadline); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddTransfer(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.FinishTransferSetup(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.TransferFailed(job.ID, errors.New("upload failed")); err != nil {
		t.Fatal(err)
	}

	manager.RunDue(context.Background(), time.Now())
	if executed != 0 {
		t.Fatalf("cleanup executed %d times while transfer was failed", executed)
	}
	blocked, ok := manager.Get(job.ID)
	if !ok || blocked.Phase != tool.CleanupBlocked || blocked.FailedTransfers != 1 {
		t.Fatalf("unexpected blocked cleanup job: %+v, exists=%v", blocked, ok)
	}

	if err := manager.RetryTransfer(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.TransferSucceeded(job.ID); err != nil {
		t.Fatal(err)
	}
	manager.RunDue(context.Background(), time.Now())
	if executed != 1 {
		t.Fatalf("cleanup executed %d times after retry succeeded, want 1", executed)
	}
	if _, ok := manager.Get(job.ID); ok {
		t.Fatal("completed cleanup job was not removed")
	}

	var persisted []tool.CleanupJob
	if err := json.Unmarshal(store.data, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 0 {
		t.Fatalf("persisted cleanup jobs = %+v, want none", persisted)
	}
}

func TestOfflineCleanupRestoresSeedingDeadlineAfterRestart(t *testing.T) {
	store := &cleanupStore{}
	deadline := time.Now().Add(time.Hour)
	first, err := tool.NewCleanupManager(store.read, store.write, func(context.Context, tool.CleanupJob) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	job := tool.CleanupJob{ID: "cleanup-restore", TempDir: "/tmp/openlist/qBittorrent/task-restore", Toolname: "qBittorrent"}
	if err := first.Register(job); err != nil {
		t.Fatal(err)
	}
	if err := first.BeginTransfer(job.ID, "gid-restore", deadline); err != nil {
		t.Fatal(err)
	}
	if err := first.AddTransfer(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := first.FinishTransferSetup(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := first.TransferSucceeded(job.ID); err != nil {
		t.Fatal(err)
	}

	executed := 0
	restored, err := tool.NewCleanupManager(store.read, store.write, func(context.Context, tool.CleanupJob) error {
		executed++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	restoredJob, ok := restored.Get(job.ID)
	if !ok || restoredJob.Phase != tool.CleanupWaitingSeeding || !restoredJob.DeleteAfterTime.Equal(deadline) {
		t.Fatalf("unexpected restored cleanup job: %+v, exists=%v", restoredJob, ok)
	}
	restored.RunDue(context.Background(), deadline.Add(time.Second))
	if executed != 1 {
		t.Fatalf("restored cleanup executed %d times, want 1", executed)
	}
}

func TestGlobalTempCleanupPreservesReferencedPaths(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, "qBittorrent", "task-1")
	orphan := filepath.Join(root, "aria2", "orphan")
	if err := os.MkdirAll(protected, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}

	store := &cleanupStore{}
	manager, err := tool.NewCleanupManager(store.read, store.write, func(context.Context, tool.CleanupJob) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(tool.CleanupJob{
		ID:       "cleanup-1",
		TempDir:  protected,
		Toolname: "qBittorrent",
	}); err != nil {
		t.Fatal(err)
	}

	oldConf := conf.Conf
	oldManager := tool.CleanupTaskManager
	conf.Conf = conf.DefaultConfig(root)
	conf.Conf.TempDir = root
	tool.CleanupTaskManager = manager
	t.Cleanup(func() {
		conf.Conf = oldConf
		tool.CleanupTaskManager = oldManager
	})

	bootstrap.CleanTempDir()
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("protected temp path was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "aria2")); !os.IsNotExist(err) {
		t.Fatalf("orphan temp path still exists or stat failed: %v", err)
	}
}
