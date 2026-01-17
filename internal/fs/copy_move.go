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
	switch t {
	case copy:
		return "copy"
	case move:
		return "move"
	case merge:
		return "merge"
	default:
		return "unknown"
	}
}

const (
	copy taskType = iota
	move
	merge
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
	if t.SrcStorage == nil {
		if srcStorage, _, err := op.GetStorageAndActualPath(t.SrcStorageMp); err == nil {
			t.SrcStorage = srcStorage
		} else {
			return err
		}
		if dstStorage, _, err := op.GetStorageAndActualPath(t.DstStorageMp); err == nil {
			t.DstStorage = dstStorage
		} else {
			return err
		}
	}

	t.ClearEndTime()
	t.SetStartTime(time.Now())
	defer func() { t.SetEndTime(time.Now()) }()
	return t.RunWithNextTaskCallback(func(nextTask *FileTransferTask) error {
		task_group.TransferCoordinator.AddTask(t.groupID, nil)
		if t.TaskType == copy || t.TaskType == merge {
			CopyTaskManager.Add(nextTask)
		} else {
			MoveTaskManager.Add(nextTask)
		}
		return nil
	})
}

func (t *FileTransferTask) OnSucceeded() {
	task_group.TransferCoordinator.Done(context.WithoutCancel(t.Ctx()), t.groupID, true)
}

func (t *FileTransferTask) OnFailed() {
	task_group.TransferCoordinator.Done(context.WithoutCancel(t.Ctx()), t.groupID, false)
}

func (t *FileTransferTask) SetRetry(retry int, maxRetry int) {
	t.TaskData.SetRetry(retry, maxRetry)
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

func transfer(ctx context.Context, taskType taskType, srcObjPath, dstDirPath string, skipHook ...bool) (task.TaskExtensionInfo, error) {
	srcStorage, srcObjActualPath, err := op.GetStorageAndActualPath(srcObjPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get src storage")
	}
	dstStorage, dstDirActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get dst storage")
	}

	if srcStorage.GetStorage() == dstStorage.GetStorage() {
		if utils.IsBool(skipHook...) {
			ctx = context.WithValue(ctx, conf.SkipHookKey, struct{}{})
		}
		if taskType == copy || taskType == merge {
			err = op.Copy(ctx, srcStorage, srcObjActualPath, dstDirActualPath)
			if !errors.Is(err, errs.NotImplement) && !errors.Is(err, errs.NotSupport) {
				return nil, err
			}
		} else {
			err = op.Move(ctx, srcStorage, srcObjActualPath, dstDirActualPath)
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

	t.groupID = stdpath.Join(t.DstStorageMp, t.DstActualPath)
	task_group.TransferCoordinator.AddTask(t.groupID, nil)
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
		if err == nil {
			hasSuccess = true
		}
		if taskType == move {
			task_group.TransferCoordinator.AppendPayload(t.groupID, task_group.SrcPathToRemove(srcObjPath))
		}
		task_group.TransferCoordinator.Done(context.WithoutCancel(ctx), t.groupID, hasSuccess)
		return nil, err
	}

	t.Creator, _ = ctx.Value(conf.UserKey).(*model.User)
	t.ApiUrl = common.GetApiUrl(ctx)
	if taskType == copy || taskType == merge {
		CopyTaskManager.Add(t)
	} else {
		task_group.TransferCoordinator.AppendPayload(t.groupID, task_group.SrcPathToRemove(srcObjPath))
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
		task_group.TransferCoordinator.AppendPayload(t.groupID, task_group.DstPathToHook(dstActualPath))

		existedObjs := make(map[string]bool)
		if t.TaskType == merge {
			dstObjs, err := op.List(t.Ctx(), t.DstStorage, dstActualPath, model.ListArgs{})
			if err != nil && !errors.Is(err, errs.ObjectNotFound) {
				// 目标文件夹不存在的情况不是错误，会在之后新建文件夹
				// 这种情况显然不需要统计existedObjs，dstObjs保持为nil，下面这个for将不会执行
				return errors.WithMessagef(err, "failed list dst [%s] objs", dstActualPath)
			}
			for _, obj := range dstObjs {
				if err := t.Ctx().Err(); err != nil {
					return err
				}
				if !obj.IsDir() {
					existedObjs[obj.GetName()] = true
				}
			}
		}

		for _, obj := range objs {
			if err := t.Ctx().Err(); err != nil {
				return err
			}

			if t.TaskType == merge && !obj.IsDir() && existedObjs[obj.GetName()] {
				// skip existed file
				continue
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
				groupID: t.groupID,
			})
			if err != nil {
				return err
			}
		}
		t.Status = fmt.Sprintf("src object is dir, added all %s tasks of objs", t.TaskType)
		return nil
	}

	t.Status = "getting src object link"
	link, srcObj, err := op.Link(t.Ctx(), t.SrcStorage, t.SrcActualPath, model.LinkArgs{})
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
	return op.Put(context.WithValue(t.Ctx(), conf.SkipHookKey, struct{}{}), t.DstStorage, t.DstActualPath, ss, t.SetProgress)
}

var (
	CopyTaskManager *tache.Manager[*FileTransferTask]
	MoveTaskManager *tache.Manager[*FileTransferTask]
)
