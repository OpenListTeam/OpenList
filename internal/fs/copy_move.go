package fs

import (
	"context"
	"fmt"
	stdpath "path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/internal/task_group"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/tache"
	"github.com/pkg/errors"
)

type taskType uint8

func (t taskType) String() string {
	if t == 0 {
		return "copy"
	} else {
		return "move"
	}
}

const (
	copy taskType = iota
	move
)

type FileTransferTask struct {
	TaskData
	TaskType taskType
	groupID  string
}

func (t *FileTransferTask) GetName() string {
	return fmt.Sprintf("%s [%s](%s) to [%s](%s)", t.TaskType, t.SrcStorageMp, t.SrcActualPath, t.DstStorageMp, t.DstActualPath)
}

func (t *FileTransferTask) Run() error {
	if err := t.ReinitCtx(); err != nil {
		return err
	}
	t.ClearEndTime()
	t.SetStartTime(time.Now())
	defer func() { t.SetEndTime(time.Now()) }()
	return t.RunWithNextTaskCallback(func(nextTask *FileTransferTask) error {
		nextTask.groupID = t.groupID
		task_group.TransferCoordinator.AddTask(t.groupID, nil)
		if t.TaskType == copy {
			CopyTaskManager.Add(nextTask)
		} else {
			MoveTaskManager.Add(nextTask)
		}
		return nil
	})
}

func (t *FileTransferTask) OnSucceeded() {
	task_group.TransferCoordinator.Done(t.groupID, true)
}

func (t *FileTransferTask) OnFailed() {
	task_group.TransferCoordinator.Done(t.groupID, false)
}

func (t *FileTransferTask) SetRetry(retry int, maxRetry int) {
	t.TaskExtension.SetRetry(retry, maxRetry)
	if retry == 0 &&
		(len(t.groupID) == 0 || // 重启恢复
			(t.GetErr() == nil && t.GetState() != tache.StatePending)) { // 手动重试
		t.groupID = stdpath.Join(t.DstStorageMp, t.DstActualPath)
		var payload any
		if t.TaskType == move {
			payload = task_group.SrcPathToRemove(stdpath.Join(t.SrcStorageMp, t.SrcActualPath))
		}
		task_group.TransferCoordinator.AddTask(t.groupID, payload)
	}
}

func transfer(ctx context.Context, taskType taskType, srcObjPath, dstDirPath string, lazyCache ...bool) (task.TaskExtensionInfo, error) {
	srcStorage, srcObjActualPath, err := op.GetStorageAndActualPath(srcObjPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get src storage")
	}
	dstStorage, dstDirActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get dst storage")
	}

	if srcStorage.GetStorage() == dstStorage.GetStorage() {
		if taskType == copy {
			err = op.Copy(ctx, srcStorage, srcObjActualPath, dstDirActualPath, lazyCache...)
			if !errors.Is(err, errs.NotImplement) && !errors.Is(err, errs.NotSupport) {
				return nil, err
			}
		} else {
			err = op.Move(ctx, srcStorage, srcObjActualPath, dstDirActualPath, lazyCache...)
			if !errors.Is(err, errs.NotImplement) && !errors.Is(err, errs.NotSupport) {
				return nil, err
			}
		}
	}

	// not in the same storage
	t := &FileTransferTask{
		TaskData: TaskData{
			SrcStorage:    srcStorage,
			DstStorage:    dstStorage,
			SrcActualPath: srcObjActualPath,
			DstActualPath: dstDirActualPath,
			SrcStorageMp:  srcStorage.GetStorage().MountPath,
			DstStorageMp:  dstStorage.GetStorage().MountPath,
		},
		TaskType: taskType,
	}

	if ctx.Value(conf.NoTaskKey) != nil {
		var callback func(nextTask *FileTransferTask) error
		hasSuccess := false
		callback = func(nextTask *FileTransferTask) error {
			nextTask.Base.SetCtx(ctx)
			err := nextTask.RunWithNextTaskCallback(callback)
			if err == nil {
				hasSuccess = true
			}
			return err
		}
		t.Base.SetCtx(ctx)
		err = t.RunWithNextTaskCallback(callback)
		if hasSuccess || err == nil {
			if taskType == move {
				task_group.RefreshAndRemove(dstDirPath, task_group.SrcPathToRemove(srcObjPath))
			} else {
				op.DeleteCache(t.DstStorage, dstDirActualPath)
			}
		}
		return nil, err
	}

	t.Creator, _ = ctx.Value(conf.UserKey).(*model.User)
	t.ApiUrl = common.GetApiUrl(ctx)
	t.groupID = dstDirPath
	if taskType == copy {
		task_group.TransferCoordinator.AddTask(dstDirPath, nil)
		CopyTaskManager.Add(t)
	} else {
		task_group.TransferCoordinator.AddTask(dstDirPath, task_group.SrcPathToRemove(srcObjPath))
		MoveTaskManager.Add(t)
	}
	return t, nil
}

func (t *FileTransferTask) RunWithNextTaskCallback(f func(nextTask *FileTransferTask) error) error {
	t.Status = "getting src object"
	srcObj, err := op.Get(t.Ctx(), t.SrcStorage, t.SrcActualPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] file", t.SrcActualPath)
	}
	if srcObj.IsDir() {
		t.Status = "src object is dir, listing objs"
		objs, err := op.List(t.Ctx(), t.SrcStorage, t.SrcActualPath, model.ListArgs{})
		if err != nil {
			return errors.WithMessagef(err, "failed list src [%s] objs", t.SrcActualPath)
		}
		dstActualPath := stdpath.Join(t.DstActualPath, srcObj.GetName())
		if t.TaskType == copy {
			if t.Ctx().Value(conf.NoTaskKey) != nil {
				defer op.DeleteCache(t.DstStorage, dstActualPath)
			} else {
				task_group.TransferCoordinator.AppendPayload(t.groupID, task_group.DstPathToRefresh(dstActualPath))
			}
		}
		for _, obj := range objs {
			if utils.IsCanceled(t.Ctx()) {
				return nil
			}
			err = f(&FileTransferTask{
				TaskType: t.TaskType,
				TaskData: TaskData{
					TaskExtension: task.TaskExtension{
						Creator: t.Creator,
						ApiUrl:  t.ApiUrl,
					},
					SrcStorage:    t.SrcStorage,
					DstStorage:    t.DstStorage,
					SrcActualPath: stdpath.Join(t.SrcActualPath, obj.GetName()),
					DstActualPath: dstActualPath,
					SrcStorageMp:  t.SrcStorageMp,
					DstStorageMp:  t.DstStorageMp,
				},
			})
			if err != nil {
				return err
			}
		}
		t.Status = fmt.Sprintf("src object is dir, added all %s tasks of objs", t.TaskType)
		return nil
	}

	link, _, err := op.Link(t.Ctx(), t.SrcStorage, t.SrcActualPath, model.LinkArgs{})
	if err != nil {
		return errors.WithMessagef(err, "failed get [%s] link", t.SrcActualPath)
	}
	// any link provided is seekable
	ss, err := stream.NewSeekableStream(&stream.FileStream{
		Obj: srcObj,
		Ctx: t.Ctx(),
	}, link)
	if err != nil {
		_ = link.Close()
		return errors.WithMessagef(err, "failed get [%s] stream", t.SrcActualPath)
	}
	t.SetTotalBytes(ss.GetSize())
	t.Status = "uploading"
	return op.Put(t.Ctx(), t.DstStorage, t.DstActualPath, ss, t.SetProgress, true)
}

var (
	CopyTaskManager *tache.Manager[*FileTransferTask]
	MoveTaskManager *tache.Manager[*FileTransferTask]
)
