package fs

import (
	"context"
	"fmt"
	stdpath "path"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/internal/task_group"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/pkg/errors"
)

func makeDir(ctx context.Context, path string, lazyCache ...bool) error {
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}
	return op.MakeDir(ctx, storage, actualPath, lazyCache...)
}

func rename(ctx context.Context, srcPath, dstName string, lazyCache ...bool) error {
	storage, srcActualPath, err := op.GetStorageAndActualPath(srcPath)
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}
	return op.Rename(ctx, storage, srcActualPath, dstName, lazyCache...)
}

func remove(ctx context.Context, path string) error {
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}
	return op.Remove(ctx, storage, actualPath)
}

func other(ctx context.Context, args model.FsOtherArgs) (interface{}, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(args.Path)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get storage")
	}
	args.Path = actualPath
	return op.Other(ctx, storage, args)
}

type Transfer struct {
	task.TaskExtension
	Status        string        `json:"-"` //don't save status to save space
	SrcActualPath string        `json:"src_path"`
	DstActualPath string        `json:"dst_path"`
	SrcStorage    driver.Driver `json:"-"`
	DstStorage    driver.Driver `json:"-"`
	SrcStorageMp  string        `json:"src_storage_mp"`
	DstStorageMp  string        `json:"dst_storage_mp"`
	GroupID       string        `json:"-"`
}

type CopyOrMoveTask struct {
	Transfer
	TaskType string // "copy" or "move"
}

// if in the same storage, call method
// if not, add task
func _copyOrMove(ctx context.Context, isCopy bool, srcObjPath, dstDirPath string, lazyCache ...bool) (task.TaskExtensionInfo, error) {
	srcStorage, srcObjActualPath, err := op.GetStorageAndActualPath(srcObjPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get src storage")
	}
	dstStorage, dstDirActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get dst storage")
	}

	if srcStorage.GetStorage() == dstStorage.GetStorage() {
		if isCopy {
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

	if ctx.Value(conf.NoTaskKey) != nil { // webdav
		srcObj, err := op.Get(ctx, srcStorage, srcObjActualPath)
		if err != nil {
			return nil, errors.WithMessagef(err, "failed get src [%s] file", srcObjPath)
		}
		if !srcObj.IsDir() {
			// copy file directly
			link, _, err := op.Link(ctx, srcStorage, srcObjActualPath, model.LinkArgs{})
			if err != nil {
				return nil, errors.WithMessagef(err, "failed get [%s] link", srcObjPath)
			}
			// any link provided is seekable
			ss, err := stream.NewSeekableStream(&stream.FileStream{
				Obj: srcObj,
				Ctx: ctx,
			}, link)
			if err != nil {
				_ = link.Close()
				return nil, errors.WithMessagef(err, "failed get [%s] stream", srcObjPath)
			}
			defer func() {
				task_group.TransferCoordinator.Done(dstDirPath, err == nil)
			}()
			if isCopy {
				task_group.TransferCoordinator.AddTask(dstDirPath, nil)
			} else {
				task_group.TransferCoordinator.AddTask(dstDirPath, task_group.SrcPathToRemove(srcObjPath))
			}
			err = op.Put(ctx, dstStorage, dstDirActualPath, ss, nil, false)
			return nil, err
		}
	}

	// not in the same storage
	taskCreator, _ := ctx.Value(conf.UserKey).(*model.User)
	t := &CopyOrMoveTask{
		Transfer: Transfer{
			TaskExtension: task.TaskExtension{
				Creator: taskCreator,
				ApiUrl:  common.GetApiUrl(ctx),
			},
			SrcStorage:    srcStorage,
			DstStorage:    dstStorage,
			SrcActualPath: srcObjActualPath,
			DstActualPath: dstDirActualPath,
			SrcStorageMp:  srcStorage.GetStorage().MountPath,
			DstStorageMp:  dstStorage.GetStorage().MountPath,
			GroupID:       dstDirPath,
		},
	}
	if isCopy {
		task_group.TransferCoordinator.AddTask(dstDirPath, nil)
		t.TaskType = "copy"
		copyTask := (*CopyTask)(t)
		CopyTaskManager.Add(copyTask)
		return copyTask, nil
	}
	task_group.TransferCoordinator.AddTask(dstDirPath, task_group.SrcPathToRemove(srcObjPath))
	t.TaskType = "move"
	moveTask := (*MoveTask)(t)
	MoveTaskManager.Add(moveTask)
	return moveTask, nil
}

func putBetween2Storages(t *CopyOrMoveTask, srcStorage, dstStorage driver.Driver, srcActualPath, dstDirActualPath string) error {
	t.Status = "getting src object"
	srcObj, err := op.Get(t.Ctx(), srcStorage, srcActualPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] file", srcActualPath)
	}
	if srcObj.IsDir() {
		t.Status = "src object is dir, listing objs"
		objs, err := op.List(t.Ctx(), srcStorage, srcActualPath, model.ListArgs{})
		if err != nil {
			return errors.WithMessagef(err, "failed list src [%s] objs", srcActualPath)
		}
		dstActualPath := stdpath.Join(dstDirActualPath, srcObj.GetName())
		if t.TaskType == "copy" {
			task_group.TransferCoordinator.AppendPayload(t.GroupID, task_group.DstPathToRefresh(dstActualPath))
		}
		for _, obj := range objs {
			if utils.IsCanceled(t.Ctx()) {
				return nil
			}
			task := &CopyOrMoveTask{
				TaskType: t.TaskType,
				Transfer: Transfer{
					TaskExtension: task.TaskExtension{
						Creator: t.GetCreator(),
						ApiUrl:  t.ApiUrl,
					},
					SrcStorage:    srcStorage,
					DstStorage:    dstStorage,
					SrcActualPath: stdpath.Join(srcActualPath, obj.GetName()),
					DstActualPath: dstActualPath,
					SrcStorageMp:  srcStorage.GetStorage().MountPath,
					DstStorageMp:  dstStorage.GetStorage().MountPath,
					GroupID:       t.GroupID,
				},
			}
			task_group.TransferCoordinator.AddTask(t.GroupID, nil)
			if t.TaskType == "copy" {
				CopyTaskManager.Add((*CopyTask)(task))
			} else {
				MoveTaskManager.Add((*MoveTask)(task))
			}
		}
		t.Status = fmt.Sprintf("src object is dir, added all %s tasks of objs", t.TaskType)
		return nil
	}
	return putFileBetween2Storages(t, srcStorage, dstStorage, srcActualPath, dstDirActualPath)
}

func putFileBetween2Storages(tsk *CopyOrMoveTask, srcStorage, dstStorage driver.Driver, srcActualPath, dstDirActualPath string) error {
	srcFile, err := op.Get(tsk.Ctx(), srcStorage, srcActualPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] file", srcActualPath)
	}
	tsk.SetTotalBytes(srcFile.GetSize())
	link, _, err := op.Link(tsk.Ctx(), srcStorage, srcActualPath, model.LinkArgs{})
	if err != nil {
		return errors.WithMessagef(err, "failed get [%s] link", srcActualPath)
	}
	// any link provided is seekable
	ss, err := stream.NewSeekableStream(&stream.FileStream{
		Obj: srcFile,
		Ctx: tsk.Ctx(),
	}, link)
	if err != nil {
		_ = link.Close()
		return errors.WithMessagef(err, "failed get [%s] stream", srcActualPath)
	}
	tsk.SetTotalBytes(ss.GetSize())
	return op.Put(tsk.Ctx(), dstStorage, dstDirActualPath, ss, tsk.SetProgress, true)
}
