package fs

import (
	"context"
	"fmt"
	stdpath "path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/internal/task_group"
	"github.com/OpenListTeam/tache"
	"github.com/pkg/errors"
)

type UploadTask struct {
	task.TaskExtension
	storage          driver.Driver
	dstDirActualPath string
	file             model.FileStreamer
}

func (t *UploadTask) GetName() string {
	return fmt.Sprintf("upload %s to [%s](%s)", t.file.GetName(), t.storage.GetStorage().MountPath, t.dstDirActualPath)
}

func (t *UploadTask) GetStatus() string {
	return "uploading"
}

func (t *UploadTask) Run() error {
	t.ClearEndTime()
	t.SetStartTime(time.Now())
	defer func() { t.SetEndTime(time.Now()) }()
	return op.Put(context.WithValue(t.Ctx(), conf.SkipHookKey, struct{}{}), t.storage, t.dstDirActualPath, t.file, t.SetProgress)
}

func (t *UploadTask) OnSucceeded() {
	task_group.TransferCoordinator.Done(context.WithoutCancel(t.Ctx()), stdpath.Join(t.storage.GetStorage().MountPath, t.dstDirActualPath), true)
}

func (t *UploadTask) OnFailed() {
	task_group.TransferCoordinator.Done(context.WithoutCancel(t.Ctx()), stdpath.Join(t.storage.GetStorage().MountPath, t.dstDirActualPath), false)
}

func (t *UploadTask) SetRetry(retry int, maxRetry int) {
	t.TaskExtension.SetRetry(retry, maxRetry)
	if retry == 0 &&
		(t.GetErr() == nil && t.GetState() != tache.StatePending) { // 手动重试
		task_group.TransferCoordinator.AddTask(stdpath.Join(t.storage.GetStorage().MountPath, t.dstDirActualPath), nil)
	}
}

var UploadTaskManager *tache.Manager[*UploadTask]

// putAsTask add as a put task and return immediately
func putAsTask(ctx context.Context, dstDirPath string, file model.FileStreamer) (task.TaskExtensionInfo, error) {
	storage, dstDirActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get storage")
	}
	if storage.Config().NoUpload {
		return nil, errors.WithStack(errs.UploadNotSupported)
	}
	if file.NeedStore() {
		_, err := file.CacheFullAndWriter(nil, nil)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create temp file")
		}
		//file.SetReader(tempFile)
		//file.SetTmpFile(tempFile)
	}
	taskCreator, _ := ctx.Value(conf.UserKey).(*model.User) // taskCreator is nil when convert failed
	t := &UploadTask{
		TaskExtension: task.TaskExtension{
			Creator: taskCreator,
			ApiUrl:  common.GetApiUrl(ctx),
		},
		storage:          storage,
		dstDirActualPath: dstDirActualPath,
		file:             file,
	}
	t.SetTotalBytes(file.GetSize())
	task_group.TransferCoordinator.AddTask(stdpath.Join(storage.GetStorage().MountPath, dstDirActualPath), nil)
	UploadTaskManager.Add(t)
	return t, nil
}

// putDirect put the file and return after finish
func putDirectly(ctx context.Context, dstDirPath string, file model.FileStreamer, skipHook ...bool) error {
	storage, dstDirActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		_ = file.Close()
		return errors.WithMessage(err, "failed get storage")
	}
	if storage.Config().NoUpload {
		_ = file.Close()
		return errors.WithStack(errs.UploadNotSupported)
	}
	if utils.IsBool(skipHook...) {
		ctx = context.WithValue(ctx, conf.SkipHookKey, struct{}{})
	}
	return op.Put(ctx, storage, dstDirActualPath, file, nil)
}

func getDirectUploadInfo(ctx context.Context, tool, dstDirPath, dstName string, fileSize int64) (any, error) {
	storage, dstDirActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get storage")
	}
	return op.GetDirectUploadInfo(ctx, tool, storage, dstDirActualPath, dstName, fileSize)
}
