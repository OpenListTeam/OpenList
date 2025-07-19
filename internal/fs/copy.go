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

type CopyTask CopyOrMoveTask

func (t *CopyTask) GetName() string {
	return fmt.Sprintf("copy [%s](%s) to [%s](%s)", t.SrcStorageMp, t.SrcActualPath, t.DstStorageMp, t.DstActualPath)
}

func (t *CopyTask) GetStatus() string {
	return t.Status
}

func (t *CopyTask) Run() error {
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

func (t *CopyTask) OnSucceeded() {
	task_group.TransferCoordinator.Done(t.GroupID, true)
}

func (t *CopyTask) OnFailed() {
	task_group.TransferCoordinator.Done(t.GroupID, false)
}

func (t *CopyTask) SetRetry(retry int, maxRetry int) {
	t.TaskExtension.SetRetry(retry, maxRetry)
	if retry == 0 &&
		(len(t.GroupID) == 0 || // 重启恢复
			(t.GetErr() == nil && t.GetState() != tache.StatePending)) { // 手动重试
		t.GroupID = stdpath.Join(t.DstStorageMp, t.DstActualPath)
		task_group.TransferCoordinator.AddTask(t.GroupID, nil)
	}
}

var CopyTaskManager *tache.Manager[*CopyTask]
