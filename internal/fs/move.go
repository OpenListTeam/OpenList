package fs

import (
	"fmt"
	stdpath "path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/task_group"
	"github.com/OpenListTeam/tache"
	"github.com/pkg/errors"
)

type MoveTask CopyOrMoveTask

func (t *MoveTask) GetName() string {
	return fmt.Sprintf("move [%s](%s) to [%s](%s)", t.SrcStorageMp, t.SrcActualPath, t.DstStorageMp, t.DstActualPath)
}

func (t *MoveTask) GetStatus() string {
	return t.Status
}

func (t *MoveTask) Run() error {
	if err := t.ReinitCtx(); err != nil {
		return err
	}
	t.ClearEndTime()
	t.SetStartTime(time.Now())
	defer func() { t.SetEndTime(time.Now()) }()
	var err error
	if t.SrcStorage == nil {
		t.SrcStorage, err = op.GetStorageByMountPath(t.SrcStorageMp)
	}
	if t.DstStorage == nil {
		t.DstStorage, err = op.GetStorageByMountPath(t.DstStorageMp)
	}
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}
	return putBetween2Storages((*CopyOrMoveTask)(t), t.SrcStorage, t.DstStorage, t.SrcActualPath, t.DstActualPath)
}

func (t *MoveTask) OnSucceeded() {
	task_group.TransferCoordinator.Done(t.GroupID, true)
}

func (t *MoveTask) OnFailed() {
	task_group.TransferCoordinator.Done(t.GroupID, false)
}

func (t *MoveTask) SetRetry(retry int, maxRetry int) {
	t.TaskExtension.SetRetry(retry, maxRetry)
	if retry == 0 &&
		(len(t.GroupID) == 0 || // 重启恢复
			(t.GetErr() == nil && t.GetState() != tache.StatePending)) { // 手动重试
		t.GroupID = stdpath.Join(t.DstStorageMp, t.DstActualPath)
		task_group.TransferCoordinator.AddTask(t.GroupID, task_group.SrcPathToRemove(stdpath.Join(t.SrcStorageMp, t.SrcActualPath)))
	}
}

var MoveTaskManager *tache.Manager[*MoveTask]
