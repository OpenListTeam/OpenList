package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/task_group"
	"github.com/OpenListTeam/tache"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestTransferBatchCancellation(t *testing.T) {
	previousConf := conf.Conf
	conf.Conf = conf.DefaultConfig(t.TempDir())
	t.Cleanup(func() { conf.Conf = previousConf })
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Init(database)
	t.Cleanup(db.Close)

	// A mounted Local driver exercises storage lookup and op.List as well as os.ReadDir.
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	addition, err := json.Marshal(map[string]string{"root_folder_path": root})
	if err != nil {
		t.Fatal(err)
	}
	const mountPath = "/transfer-batch-test"
	id, err := op.CreateStorage(context.Background(), model.Storage{
		Driver: "Local", MountPath: mountPath, Addition: string(addition),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := op.DeleteStorageById(context.Background(), id); err != nil {
			t.Error(err)
		}
	})

	previousManager, previousCoordinator := TransferTaskManager, task_group.TransferCoordinator
	t.Cleanup(func() {
		TransferTaskManager, task_group.TransferCoordinator = previousManager, previousCoordinator
	})
	for _, transfer := range []struct {
		name   string
		source string
		run    func(context.Context, string, string, DeletePolicy) error
	}{
		{"std", root, transferStd},
		{"obj", mountPath, transferObj},
	} {
		for _, tc := range []struct {
			name         string
			cancelAtOne  bool
			wantEnqueued int
		}{
			{"complete", false, 3},
			{"cancel_after_first", true, 1},
		} {
			t.Run(transfer.name+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				task_group.TransferCoordinator = task_group.NewTaskGroupCoordinator("test", nil)
				// Persist synchronously after the first real Add, with workers paused,
				// so cancellation happens inside the batch without timing assumptions.
				TransferTaskManager = tache.NewManager[*TransferTask](
					tache.WithRunning(false),
					func(opts *tache.Options) { opts.PersistDebounce = nil },
					tache.WithPersistFunction(
						func() ([]byte, error) { return []byte("[]"), nil },
						func([]byte) error {
							if tc.cancelAtOne && len(TransferTaskManager.GetAll()) == 1 {
								cancel()
							}
							return nil
						},
					),
				)
				t.Cleanup(TransferTaskManager.CancelAll)
				err := transfer.run(ctx, transfer.source, mountPath, DeleteNever)
				if tc.cancelAtOne {
					if !errors.Is(err, context.Canceled) {
						t.Errorf("transfer error = %v, want context.Canceled", err)
					}
				} else if err != nil {
					t.Errorf("transfer error = %v", err)
				}
				tasks := TransferTaskManager.GetAll()
				if len(tasks) != tc.wantEnqueued {
					t.Fatalf("enqueued %d tasks, want %d", len(tasks), tc.wantEnqueued)
				}
				for _, task := range tasks {
					if task.GetState() != tache.StatePending || task.Ctx().Err() != nil {
						t.Errorf("enqueued task state = %v, context error = %v", task.GetState(), task.Ctx().Err())
					}
				}
			})
		}
	}
}
