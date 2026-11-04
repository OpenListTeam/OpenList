package fs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/OpenListTeam/tache"
)

func TestSeedSaveTaskFinishPreservesCancellation(t *testing.T) {
	for _, state := range []tache.State{tache.StateRunning, tache.StateCanceling, tache.StateCanceled} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			task := &SeedSaveTask{}
			task.SetCtx(ctx)
			task.SetState(state)
			task.Finish(nil)
			if task.GetState() != tache.StateCanceled || task.GetStatus() != "已取消" {
				t.Fatalf("state=%v status=%q", task.GetState(), task.GetStatus())
			}
		})
	}
}

func TestSeedSaveTaskFinishReportsResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want tache.State
	}{
		{"success", nil, tache.StateSucceeded},
		{"failure", errors.New("save failed"), tache.StateFailed},
		{"cancellation", context.Canceled, tache.StateCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := &SeedSaveTask{}
			task.SetCtx(context.Background())
			task.SetState(tache.StateRunning)
			task.Finish(tc.err)
			if task.GetState() != tc.want || task.GetEndTime() == nil {
				t.Fatalf("state=%v end=%v", task.GetState(), task.GetEndTime())
			}
		})
	}
}
