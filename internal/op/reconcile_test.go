package op

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/mq"
)

func TestNamespaceMutationDefersReconciliationThroughCompensation(t *testing.T) {
	previousCache := Cache
	Cache = NewCacheManager()
	Cache.SetSetting(conf.HandleHookAfterWriting, &model.SettingItem{Value: "true"})
	previousProjector := snapshotProjector
	SetSnapshotProjector(func(context.Context, string, []model.Obj) {
		t.Error("an intermediate list was published")
	})
	processed := make(chan reconciliationTarget, 4)
	processor := mq.NewLatestProcessor(4, 4, 1, func(_ context.Context, _ string, target reconciliationTarget) {
		processed <- target
	})
	previousProcessor := reconciliation.Swap(processor)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		if err := processor.Stop(ctx); err != nil {
			t.Error(err)
		}
		reconciliation.Store(previousProcessor)
		SetSnapshotProjector(previousProjector)
		Cache = previousCache
	})

	source := newListIdentityDriver("/source", func(model.ListArgs) []model.Obj {
		return []model.Obj{&model.Object{Name: "staging"}}
	})
	destination := newListIdentityDriver("/destination", nil)
	cause := errors.New("commit failed")
	canceled, cancel := context.WithCancel(context.Background())
	err := RunNamespaceMutation(canceled, func(ctx context.Context) error {
		mutationCtx, finish := enterMutationFrame(ctx, source, reconciliationTarget{path: "/"})
		commitNamespaceMutation(mutationCtx, source, []string{Key(source, "/")}, func() {})
		finish()
		if _, err := List(ctx, source, "/", model.ListArgs{Refresh: true}); err != nil {
			return err
		}
		cancel()
		return RunNamespaceMutation(context.WithoutCancel(ctx), func(ctx context.Context) error {
			// Compensation touches a second storage and repeats the source target.
			ScheduleSnapshotReconciliation(ctx, source, "/", false)
			ScheduleSnapshotReconciliation(ctx, destination, "/restored", true)
			if stats := processor.Stats(); stats.Pending != 0 || stats.InFlight != 0 {
				t.Fatal("reconciliation escaped before compensation completed")
			}
			return cause
		})
	})
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want commit failure", err)
	}
	seen := make(map[string]bool)
	for range 2 {
		select {
		case target := <-processed:
			seen[Key(target.storage, target.path)] = target.recursive
		case <-time.After(time.Second * 5):
			t.Fatal("completed mutation did not submit both storages")
		}
	}
	if recursive, ok := seen[Key(source, "/")]; !ok || recursive {
		t.Fatalf("source targets = %v", seen)
	}
	if !seen[Key(destination, "/restored")] {
		t.Fatalf("destination targets = %v", seen)
	}
	select {
	case target := <-processed:
		t.Fatalf("duplicate target: %v", target)
	default:
	}
}
