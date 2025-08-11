package fs

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	stdpath "path"
	"path/filepath"
	"strings"
	"time"

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
	"github.com/OpenListTeam/tache"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type ArchiveDownloadTask struct {
	TaskData
	model.ArchiveDecompressArgs
}

func (t *ArchiveDownloadTask) GetName() string {
	return fmt.Sprintf("decompress [%s](%s)[%s] to [%s](%s) with password <%s>", t.SrcStorageMp, t.SrcActualPath,
		t.InnerPath, t.DstStorageMp, t.DstActualPath, t.Password)
}

func (t *ArchiveDownloadTask) Run() error {
	if err := t.ReinitCtx(); err != nil {
		return err
	}
	t.ClearEndTime()
	t.SetStartTime(time.Now())
	defer func() { t.SetEndTime(time.Now()) }()
	uploadTask, err := t.RunWithoutPushUploadTask()
	if err != nil {
		return err
	}
	uploadTask.groupID = stdpath.Join(uploadTask.DstStorageMp, uploadTask.DstActualPath)
	task_group.TransferCoordinator.AddTask(uploadTask.groupID, nil)
	ArchiveContentUploadTaskManager.Add(uploadTask)
	return nil
}

func (t *ArchiveDownloadTask) RunWithoutPushUploadTask() (*ArchiveContentUploadTask, error) {
	srcObj, tool, ss, err := op.GetArchiveToolAndStream(t.Ctx(), t.SrcStorage, t.SrcActualPath, model.LinkArgs{})
	if err != nil {
		return nil, err
	}
	defer func() {
		var e error
		for _, s := range ss {
			e = stderrors.Join(e, s.Close())
		}
		if e != nil {
			log.Errorf("failed to close file streamer, %v", e)
		}
	}()
	var decompressUp model.UpdateProgress
	if t.CacheFull {
		total := int64(0)
		for _, s := range ss {
			total += s.GetSize()
		}
		t.SetTotalBytes(total)
		t.Status = "getting src object"
		part := 100 / float64(len(ss)+1)
		for i, s := range ss {
			if s.GetFile() != nil {
				continue
			}
			_, err = s.CacheFullAndWriter(nil, nil)
			if err != nil {
				return nil, err
			} else {
				t.SetProgress(float64(i+1) * part)
			}
		}
		decompressUp = model.UpdateProgressWithRange(t.SetProgress, 100-part, 100)
	} else {
		decompressUp = t.SetProgress
	}
	t.Status = "walking and decompressing"
	dir, err := os.MkdirTemp(conf.Conf.TempDir, "dir-*")
	if err != nil {
		return nil, err
	}
	err = tool.Decompress(ss, dir, t.ArchiveInnerArgs, decompressUp)
	if err != nil {
		return nil, err
	}
	baseName := strings.TrimSuffix(srcObj.GetName(), stdpath.Ext(srcObj.GetName()))
	uploadTask := &ArchiveContentUploadTask{
		TaskExtension: task.TaskExtension{
			Creator: t.Creator,
			ApiUrl:  t.ApiUrl,
		},
		ObjName:       baseName,
		InPlace:       !t.PutIntoNewDir,
		FilePath:      dir,
		DstActualPath: t.DstActualPath,
		dstStorage:    t.DstStorage,
		DstStorageMp:  t.DstStorageMp,
	}
	return uploadTask, nil
}

var ArchiveDownloadTaskManager *tache.Manager[*ArchiveDownloadTask]

type ArchiveContentUploadTask struct {
	task.TaskExtension
	status        string
	ObjName       string
	InPlace       bool
	FilePath      string
	DstActualPath string
	dstStorage    driver.Driver
	DstStorageMp  string
	finalized     bool
	groupID       string
}

func (t *ArchiveContentUploadTask) GetName() string {
	return fmt.Sprintf("upload %s to [%s](%s)", t.ObjName, t.DstStorageMp, t.DstActualPath)
}

func (t *ArchiveContentUploadTask) GetStatus() string {
	return t.status
}

func (t *ArchiveContentUploadTask) Run() error {
	if err := t.ReinitCtx(); err != nil {
		return err
	}
	t.ClearEndTime()
	t.SetStartTime(time.Now())
	defer func() { t.SetEndTime(time.Now()) }()
	return t.RunWithNextTaskCallback(func(nextTsk *ArchiveContentUploadTask) error {
		ArchiveContentUploadTaskManager.Add(nextTsk)
		return nil
	})
}

func (t *ArchiveContentUploadTask) OnSucceeded() {
	task_group.TransferCoordinator.Done(t.groupID, true)
}

func (t *ArchiveContentUploadTask) OnFailed() {
	task_group.TransferCoordinator.Done(t.groupID, false)
}

func (t *ArchiveContentUploadTask) SetRetry(retry int, maxRetry int) {
	t.TaskExtension.SetRetry(retry, maxRetry)
	if retry == 0 &&
		(len(t.groupID) == 0 || // 重启恢复
			(t.GetErr() == nil && t.GetState() != tache.StatePending)) { // 手动重试
		t.groupID = stdpath.Join(t.DstStorageMp, t.DstActualPath)
		task_group.TransferCoordinator.AddTask(t.groupID, nil)
	}
}

func (t *ArchiveContentUploadTask) RunWithNextTaskCallback(f func(nextTask *ArchiveContentUploadTask) error) error {
	info, err := os.Stat(t.FilePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		t.status = "src object is dir, listing objs"
		nextDstActualPath := t.DstActualPath
		if !t.InPlace {
			nextDstActualPath = stdpath.Join(nextDstActualPath, t.ObjName)
			err = op.MakeDir(t.Ctx(), t.dstStorage, nextDstActualPath)
			if err != nil {
				return err
			}
		}
		entries, err := os.ReadDir(t.FilePath)
		if err != nil {
			return err
		}
		if !t.InPlace && len(t.groupID) > 0 {
			task_group.TransferCoordinator.AppendPayload(t.groupID, task_group.DstPathToRefresh(nextDstActualPath))
		}
		var es error
		for _, entry := range entries {
			var nextFilePath string
			if entry.IsDir() {
				nextFilePath, err = moveToTempPath(stdpath.Join(t.FilePath, entry.Name()), "dir-")
			} else {
				nextFilePath, err = moveToTempPath(stdpath.Join(t.FilePath, entry.Name()), "file-")
			}
			if err != nil {
				es = stderrors.Join(es, err)
				continue
			}
			if len(t.groupID) > 0 {
				task_group.TransferCoordinator.AddTask(t.groupID, nil)
			}
			err = f(&ArchiveContentUploadTask{
				TaskExtension: task.TaskExtension{
					Creator: t.Creator,
					ApiUrl:  t.ApiUrl,
				},
				ObjName:       entry.Name(),
				InPlace:       false,
				FilePath:      nextFilePath,
				DstActualPath: nextDstActualPath,
				dstStorage:    t.dstStorage,
				DstStorageMp:  t.DstStorageMp,
				groupID:       t.groupID,
			})
			if err != nil {
				es = stderrors.Join(es, err)
			}
		}
		if es != nil {
			return es
		}
	} else {
		file, err := os.Open(t.FilePath)
		if err != nil {
			return err
		}
		t.SetTotalBytes(info.Size())
		fs := &stream.FileStream{
			Obj: &model.Object{
				Name:     t.ObjName,
				Size:     info.Size(),
				Modified: time.Now(),
			},
			Mimetype:     utils.GetMimeType(stdpath.Ext(t.ObjName)),
			WebPutAsTask: true,
			Reader:       file,
		}
		fs.Closers.Add(file)
		t.status = "uploading"
		err = op.Put(t.Ctx(), t.dstStorage, t.DstActualPath, fs, t.SetProgress, true)
		if err != nil {
			return err
		}
	}
	t.deleteSrcFile()
	return nil
}

func (t *ArchiveContentUploadTask) Cancel() {
	t.TaskExtension.Cancel()
	if !conf.Conf.Tasks.AllowRetryCanceled {
		t.deleteSrcFile()
	}
}

func (t *ArchiveContentUploadTask) deleteSrcFile() {
	if !t.finalized {
		_ = os.RemoveAll(t.FilePath)
		t.finalized = true
	}
}

func moveToTempPath(path, prefix string) (string, error) {
	newPath, err := genTempFileName(prefix)
	if err != nil {
		return "", err
	}
	err = os.Rename(path, newPath)
	if err != nil {
		return "", err
	}
	return newPath, nil
}

func genTempFileName(prefix string) (string, error) {
	retry := 0
	t := time.Now().UnixMilli()
	for retry < 10000 {
		newPath := filepath.Join(conf.Conf.TempDir, prefix+fmt.Sprintf("%x-%x", t, rand.Uint32()))
		if _, err := os.Stat(newPath); err != nil {
			if os.IsNotExist(err) {
				return newPath, nil
			} else {
				return "", err
			}
		}
		retry++
	}
	return "", errors.New("failed to generate temp-file name: too many retries")
}

type archiveContentUploadTaskManagerType struct {
	*tache.Manager[*ArchiveContentUploadTask]
}

func (m *archiveContentUploadTaskManagerType) Remove(id string) {
	if t, ok := m.GetByID(id); ok {
		t.deleteSrcFile()
		m.Manager.Remove(id)
	}
}

func (m *archiveContentUploadTaskManagerType) RemoveAll() {
	tasks := m.GetAll()
	for _, t := range tasks {
		m.Remove(t.GetID())
	}
}

func (m *archiveContentUploadTaskManagerType) RemoveByState(state ...tache.State) {
	tasks := m.GetByState(state...)
	for _, t := range tasks {
		m.Remove(t.GetID())
	}
}

func (m *archiveContentUploadTaskManagerType) RemoveByCondition(condition func(task *ArchiveContentUploadTask) bool) {
	tasks := m.GetByCondition(condition)
	for _, t := range tasks {
		m.Remove(t.GetID())
	}
}

var ArchiveContentUploadTaskManager = &archiveContentUploadTaskManagerType{
	Manager: nil,
}

func archiveMeta(ctx context.Context, path string, args model.ArchiveMetaArgs) (*model.ArchiveMetaProvider, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get storage")
	}
	return op.GetArchiveMeta(ctx, storage, actualPath, args)
}

func archiveList(ctx context.Context, path string, args model.ArchiveListArgs) ([]model.Obj, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get storage")
	}
	return op.ListArchive(ctx, storage, actualPath, args)
}

func archiveDecompress(ctx context.Context, srcObjPath, dstDirPath string, args model.ArchiveDecompressArgs, lazyCache ...bool) (task.TaskExtensionInfo, error) {
	srcStorage, srcObjActualPath, err := op.GetStorageAndActualPath(srcObjPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get src storage")
	}
	dstStorage, dstDirActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get dst storage")
	}
	if srcStorage.GetStorage() == dstStorage.GetStorage() {
		err = op.ArchiveDecompress(ctx, srcStorage, srcObjActualPath, dstDirActualPath, args, lazyCache...)
		if !errors.Is(err, errs.NotImplement) {
			return nil, err
		}
	}
	tsk := &ArchiveDownloadTask{
		TaskData: TaskData{
			SrcStorage:    srcStorage,
			DstStorage:    dstStorage,
			SrcActualPath: srcObjActualPath,
			DstActualPath: dstDirActualPath,
			SrcStorageMp:  srcStorage.GetStorage().MountPath,
			DstStorageMp:  dstStorage.GetStorage().MountPath,
		},
		ArchiveDecompressArgs: args,
	}
	if ctx.Value(conf.NoTaskKey) != nil {
		tsk.Base.SetCtx(ctx)
		uploadTask, err := tsk.RunWithoutPushUploadTask()
		if err != nil {
			return nil, errors.WithMessagef(err, "failed download [%s]", srcObjPath)
		}
		defer uploadTask.deleteSrcFile()
		var callback func(t *ArchiveContentUploadTask) error
		callback = func(t *ArchiveContentUploadTask) error {
			t.Base.SetCtx(ctx)
			e := t.RunWithNextTaskCallback(callback)
			t.deleteSrcFile()
			return e
		}
		uploadTask.Base.SetCtx(ctx)
		return nil, uploadTask.RunWithNextTaskCallback(callback)
	} else {
		tsk.Creator, _ = ctx.Value(conf.UserKey).(*model.User)
		tsk.ApiUrl = common.GetApiUrl(ctx)
		ArchiveDownloadTaskManager.Add(tsk)
		return tsk, nil
	}
}

func archiveDriverExtract(ctx context.Context, path string, args model.ArchiveInnerArgs) (*model.Link, model.Obj, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return nil, nil, errors.WithMessage(err, "failed get storage")
	}
	return op.DriverExtract(ctx, storage, actualPath, args)
}

func archiveInternalExtract(ctx context.Context, path string, args model.ArchiveInnerArgs) (io.ReadCloser, int64, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return nil, 0, errors.WithMessage(err, "failed get storage")
	}
	return op.InternalExtract(ctx, storage, actualPath, args)
}
