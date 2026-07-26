package handles

import (
	"fmt"
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
	count := applyPathBatch(m, false, other.ID, "/target", pathBatchOp[*pathTestTask]{
		filter: func(*pathTestTask) bool { return true },
		apply: func(manager task.Manager[*pathTestTask], tk *pathTestTask) {
			if !argsContains(tk.GetState(), taskDoneStates...) {
				manager.Cancel(tk.GetID())
			}
			manager.Remove(tk.GetID())
		},
	})
	if count != 1 {
		t.Fatalf("non-admin delete count = %d want 1", count)
	}
	if _, ok := m.GetByID(otherUser.GetID()); ok {
		t.Fatal("other user matched task should be removed")
	}
	if _, ok := m.GetByID(matchPending.GetID()); !ok {
		t.Fatal("admin task must remain for non-admin delete")
	}

	// admin cancel by path: only unfinished matching
	count = applyPathBatch(m, true, admin.ID, "/target", pathBatchOp[*pathTestTask]{
		filter: func(tk *pathTestTask) bool {
			return !argsContains(tk.GetState(), taskDoneStates...)
		},
		apply: func(manager task.Manager[*pathTestTask], tk *pathTestTask) {
			manager.Cancel(tk.GetID())
		},
	})
	if count != 1 {
		t.Fatalf("cancel count = %d want 1 (pending only; failed is done-state)", count)
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
	count = applyPathBatch(m, true, admin.ID, "/target", pathBatchOp[*pathTestTask]{
		filter: func(tk *pathTestTask) bool {
			return tk.GetState() == tache.StateFailed
		},
		apply: func(manager task.Manager[*pathTestTask], tk *pathTestTask) {
			manager.Retry(tk.GetID())
		},
	})
	if count != 1 {
		t.Fatalf("retry count = %d want 1", count)
	}
	if matchFailed.GetState() != tache.StateWaitingRetry {
		t.Fatalf("failed after retry state = %v want waiting_retry", matchFailed.GetState())
	}

	// delete by path: cancel then remove all matching
	count = applyPathBatch(m, true, admin.ID, "/target", pathBatchOp[*pathTestTask]{
		filter: func(*pathTestTask) bool { return true },
		apply: func(manager task.Manager[*pathTestTask], tk *pathTestTask) {
			if !argsContains(tk.GetState(), taskDoneStates...) {
				manager.Cancel(tk.GetID())
			}
			manager.Remove(tk.GetID())
		},
	})
	// matchPending, matchDstDone, matchFailed still present under /target
	if count != 3 {
		t.Fatalf("delete count = %d want 3", count)
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
	count := applyPathBatch(m, true, admin.ID, "/bulk", pathBatchOp[*pathTestTask]{
		filter: func(*pathTestTask) bool { return true },
		apply: func(manager task.Manager[*pathTestTask], tk *pathTestTask) {
			if !argsContains(tk.GetState(), taskDoneStates...) {
				manager.Cancel(tk.GetID())
			}
			manager.Remove(tk.GetID())
		},
	})
	elapsed := time.Since(start)
	if count != matchN {
		t.Fatalf("count = %d want %d", count, matchN)
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
