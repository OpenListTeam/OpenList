package handles

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/tache"
)

func ensureTaskTestConf() {
	if conf.Conf == nil {
		conf.Conf = conf.DefaultConfig(".")
	}
}

type pathTestTask struct {
	task.TaskExtension
	src string
	dst string
}

func (t *pathTestTask) GetName() string    { return "path-test" }
func (t *pathTestTask) GetStatus() string  { return "" }
func (t *pathTestTask) Run() error         { return nil }
func (t *pathTestTask) GetSrcPath() string { return t.src }
func (t *pathTestTask) GetDstPath() string { return t.dst }

func newPathManager(workers int) *tache.Manager[*pathTestTask] {
	return tache.NewManager[*pathTestTask](
		tache.WithWorks(workers),
		tache.WithRunning(false),
	)
}

func addPathTask(m *tache.Manager[*pathTestTask], creator *model.User, src, dst string, state tache.State) *pathTestTask {
	t := &pathTestTask{
		TaskExtension: task.TaskExtension{Creator: creator},
		src:           src,
		dst:           dst,
	}
	t.SetState(state)
	m.Add(t)
	return t
}

func TestResolveTaskPathPrefix(t *testing.T) {
	admin := &model.User{ID: 1, Role: model.ADMIN, BasePath: "/"}
	user := &model.User{ID: 2, Role: model.GENERAL, BasePath: "/home/user"}

	got, err := resolveTaskPathPrefix(admin, "  /Storage/a  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Storage/a" {
		t.Fatalf("admin path = %q", got)
	}

	got, err = resolveTaskPathPrefix(user, "folder")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/user/folder" {
		t.Fatalf("user path = %q", got)
	}

	if _, err := resolveTaskPathPrefix(admin, "   "); err == nil {
		t.Fatal("expected empty path error")
	}
	for _, ambiguousRoot := range []string{".", "//", "///", "./"} {
		if _, err := resolveTaskPathPrefix(admin, ambiguousRoot); !errors.Is(err, errImplicitRootTaskPath) {
			t.Fatalf("resolve %q error = %v, want %v", ambiguousRoot, err, errImplicitRootTaskPath)
		}
	}
	if got, err := resolveTaskPathPrefix(admin, "/"); err != nil || got != "/" {
		t.Fatalf("explicit root = %q, %v", got, err)
	}
	if got, err := resolveTaskPathPrefix(user, "."); err != nil || got != "/home/user" {
		t.Fatalf("user base path = %q, %v", got, err)
	}
}

func TestApplyPathBatchDeleteCancelRetry(t *testing.T) {
	ensureTaskTestConf()
	admin := &model.User{ID: 1, Role: model.ADMIN}
	other := &model.User{ID: 2, Role: model.GENERAL}
	m := newPathManager(1)

	keep := addPathTask(m, admin, "/keep/src", "/keep/dst", tache.StateSucceeded)
	matchPending := addPathTask(m, admin, "/target/a", "/other", tache.StatePending)
	matchDstDone := addPathTask(m, admin, "", "/target/b/c", tache.StateSucceeded)
	matchFailed := addPathTask(m, admin, "", "/target/fail", tache.StateFailed)
	otherUser := addPathTask(m, other, "", "/target/secret", tache.StatePending)
	falsePrefix := addPathTask(m, admin, "", "/targetx/x", tache.StatePending)

	// non-admin can only touch own tasks
	result := applyPathBatch(m, false, other.ID, "/target", pathBatchOp[*pathTestTask]{
		filter: func(*pathTestTask) bool { return true },
		apply: func(manager task.Manager[*pathTestTask], tk *pathTestTask) bool {
			if !argsContains(tk.GetState(), taskDoneStates...) {
				manager.Cancel(tk.GetID())
			}
			manager.Remove(tk.GetID())
			return true
		},
	})
	if result != (TaskPathResult{Matched: 1, Processed: 1}) {
		t.Fatalf("non-admin delete result = %+v", result)
	}
	if _, ok := m.GetByID(otherUser.GetID()); ok {
		t.Fatal("other user matched task should be removed")
	}
	if _, ok := m.GetByID(matchPending.GetID()); !ok {
		t.Fatal("admin task must remain for non-admin delete")
	}

	// admin cancel by path: only unfinished matching
	result = applyPathBatch(m, true, admin.ID, "/target", pathBatchOp[*pathTestTask]{
		filter: func(tk *pathTestTask) bool {
			return !argsContains(tk.GetState(), taskDoneStates...)
		},
		apply: func(manager task.Manager[*pathTestTask], tk *pathTestTask) bool {
			manager.Cancel(tk.GetID())
			return true
		},
	})
	if result != (TaskPathResult{Matched: 1, Processed: 1}) {
		t.Fatalf("cancel result = %+v", result)
	}
	// pending becomes canceling/canceled; failed stays failed; done stays
	if st := matchPending.GetState(); st != tache.StateCanceling && st != tache.StateCanceled {
		t.Fatalf("pending state after cancel = %v", st)
	}
	if matchFailed.GetState() != tache.StateFailed {
		t.Fatalf("failed should not be canceled, got %v", matchFailed.GetState())
	}
	if matchDstDone.GetState() != tache.StateSucceeded {
		t.Fatalf("succeeded should stay, got %v", matchDstDone.GetState())
	}

	// retry by path: only failed
	result = applyPathBatch(m, true, admin.ID, "/target", pathBatchOp[*pathTestTask]{
		filter: func(tk *pathTestTask) bool {
			return tk.GetState() == tache.StateFailed
		},
		apply: func(manager task.Manager[*pathTestTask], tk *pathTestTask) bool {
			manager.Retry(tk.GetID())
			return true
		},
	})
	if result != (TaskPathResult{Matched: 1, Processed: 1}) {
		t.Fatalf("retry result = %+v", result)
	}
	if matchFailed.GetState() != tache.StateWaitingRetry {
		t.Fatalf("failed after retry state = %v want waiting_retry", matchFailed.GetState())
	}

	// delete by path: cancel then remove all matching
	result = deletePathBatch[*pathTestTask](m, true, admin.ID, "/target")
	// matchPending, matchDstDone, matchFailed still present under /target
	if result != (TaskPathResult{Matched: 3, Processed: 3}) {
		t.Fatalf("delete result = %+v", result)
	}
	for _, id := range []string{matchPending.GetID(), matchDstDone.GetID(), matchFailed.GetID()} {
		if _, ok := m.GetByID(id); ok {
			t.Fatalf("task %s should be removed", id)
		}
	}
	if _, ok := m.GetByID(keep.GetID()); !ok {
		t.Fatal("unrelated task removed")
	}
	if _, ok := m.GetByID(falsePrefix.GetID()); !ok {
		t.Fatal("false-prefix task removed")
	}
}

func TestApplyPathBatchLargeScale(t *testing.T) {
	ensureTaskTestConf()
	admin := &model.User{ID: 1, Role: model.ADMIN}
	m := newPathManager(1)

	const total = 10000
	const matchN = 3000
	for i := 0; i < total; i++ {
		dst := fmt.Sprintf("/other/%d", i)
		state := tache.StateSucceeded
		if i < matchN {
			dst = fmt.Sprintf("/bulk/item/%d", i)
			if i%3 == 0 {
				state = tache.StatePending
			} else if i%3 == 1 {
				state = tache.StateFailed
			}
		}
		addPathTask(m, admin, "", dst, state)
	}

	start := time.Now()
	result := deletePathBatch[*pathTestTask](m, true, admin.ID, "/bulk")
	elapsed := time.Since(start)
	if result != (TaskPathResult{Matched: matchN, Processed: matchN}) {
		t.Fatalf("result = %+v", result)
	}
	remain := len(m.GetAll())
	if remain != total-matchN {
		t.Fatalf("remain = %d want %d", remain, total-matchN)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("delete %d of %d too slow: %s", matchN, total, elapsed)
	}
	t.Logf("deleted %d/%d tasks in %s", matchN, total, elapsed)
}

func TestApplyPathBatchDistinguishesMatchedAndProcessed(t *testing.T) {
	ensureTaskTestConf()
	admin := &model.User{ID: 1, Role: model.ADMIN}
	m := newPathManager(1)
	selected := addPathTask(m, admin, "", "/target/race", tache.StateFailed)

	result := applyPathBatch(m, true, admin.ID, "/target", pathBatchOp[*pathTestTask]{
		filter: func(*pathTestTask) bool { return true },
		apply: func(manager task.Manager[*pathTestTask], _ *pathTestTask) bool {
			// Simulate another request removing the task after initial matching.
			manager.Remove(selected.GetID())
			_, stillExists := manager.GetByID(selected.GetID())
			return stillExists
		},
	})
	if result != (TaskPathResult{Matched: 1, Processed: 0}) {
		t.Fatalf("result = %+v, want matched=1 processed=0", result)
	}
}

type runningPathTestTask struct {
	pathTestTask
	started chan struct{}
	stopped chan struct{}
	cleaned chan struct{}
	active  atomic.Bool
}

func (t *runningPathTestTask) Run() error {
	t.active.Store(true)
	defer t.active.Store(false)
	t.SetStartTime(time.Now())
	close(t.started)
	<-t.Ctx().Done()
	close(t.stopped)
	return context.Canceled
}

func (t *runningPathTestTask) OnFailed() {
	// Cleanup deliberately takes time so deletePathBatch must wait for it.
	time.Sleep(25 * time.Millisecond)
	close(t.cleaned)
}

func TestDeletePathBatchWaitsForRunningTaskCleanup(t *testing.T) {
	ensureTaskTestConf()
	admin := &model.User{ID: 1, Role: model.ADMIN}
	m := tache.NewManager[*runningPathTestTask](tache.WithWorks(1), tache.WithRunning(true))
	running := &runningPathTestTask{
		pathTestTask: pathTestTask{
			TaskExtension: task.TaskExtension{Creator: admin},
			dst:           "/target/running",
		},
		started: make(chan struct{}),
		stopped: make(chan struct{}),
		cleaned: make(chan struct{}),
	}
	m.Add(running)

	select {
	case <-running.started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}

	result := deletePathBatch[*runningPathTestTask](m, true, admin.ID, "/target")
	if result != (TaskPathResult{Matched: 1, Processed: 1}) {
		t.Fatalf("result = %+v", result)
	}
	select {
	case <-running.stopped:
	default:
		t.Fatal("task was removed before execution stopped")
	}
	select {
	case <-running.cleaned:
	default:
		t.Fatal("task was removed before cleanup completed")
	}
	if _, ok := m.GetByID(running.GetID()); ok {
		t.Fatal("cleaned task was not removed")
	}

	if running.active.Load() {
		t.Fatal("task execution is still active after removal")
	}
	time.Sleep(50 * time.Millisecond)
	if running.active.Load() {
		t.Fatal("task continued running in the background after removal")
	}
	if running.GetState() != tache.StateCanceled {
		t.Fatalf("final state = %v, want canceled", running.GetState())
	}
}

func TestTaskOwnedBy(t *testing.T) {
	admin := &model.User{ID: 1, Role: model.ADMIN}
	user := &model.User{ID: 2, Role: model.GENERAL}
	tk := &pathTestTask{TaskExtension: task.TaskExtension{Creator: user}}

	if !taskOwnedBy(tk, true, admin.ID) {
		t.Fatal("admin should own all")
	}
	if !taskOwnedBy(tk, false, user.ID) {
		t.Fatal("owner should own")
	}
	if taskOwnedBy(tk, false, admin.ID) {
		t.Fatal("non-owner must not own")
	}
	nilCreator := &pathTestTask{}
	if taskOwnedBy(nilCreator, false, user.ID) {
		t.Fatal("nil creator must not match non-admin")
	}
}
