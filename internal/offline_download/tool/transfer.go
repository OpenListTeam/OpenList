package tool

import (
	"context"
	"fmt"
	"os"
	"path"
	stdpath "path"
	"path/filepath"
	"time"

	_189pc "github.com/OpenListTeam/OpenList/v4/drivers/189pc"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/internal/task_group"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/tache"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type TransferTask struct {
	fs.TaskData
	DeletePolicy DeletePolicy `json:"delete_policy"`
	Url          string       `json:"url"`
	groupID      string       `json:"-"`
}

func (t *TransferTask) Run() error {
	if t.SrcStorage == nil && t.SrcStorageMp != "" {
		if srcStorage, _, err := op.GetStorageAndActualPath(t.SrcStorageMp); err == nil {
			t.SrcStorage = srcStorage
		} else {
			return err
		}
		if t.DstStorage == nil {
			if dstStorage, _, err := op.GetStorageAndActualPath(t.DstStorageMp); err == nil {
				t.DstStorage = dstStorage
			} else {
				return err
			}
		}
	}
	t.ClearEndTime()
	t.SetStartTime(time.Now())
	defer func() { t.SetEndTime(time.Now()) }()
	if t.SrcStorage == nil {
		if t.DeletePolicy == UploadDownloadStream {
			rr, err := stream.GetRangeReaderFromLink(t.GetTotalBytes(), &model.Link{URL: t.Url})
			if err != nil {
				return err
			}
			r, err := rr.RangeRead(t.Ctx(), http_range.Range{Length: t.GetTotalBytes()})
			if err != nil {
				return err
			}
			name := t.SrcActualPath
			mimetype := utils.GetMimeType(name)
			s := &stream.FileStream{
				Ctx: t.Ctx(),
				Obj: &model.Object{
					Name:     name,
					Size:     t.GetTotalBytes(),
					Modified: time.Now(),
					IsFolder: false,
				},
				Reader:   r,
				Mimetype: mimetype,
				Closers:  utils.NewClosers(r),
			}
			return op.Put(context.WithValue(t.Ctx(), conf.SkipHookKey, struct{}{}), t.DstStorage, t.DstActualPath, s, t.SetProgress)
		}
		return transferStdPath(t)
	}
	return transferObjPath(t)
}

func (t *TransferTask) GetName() string {
	if t.DeletePolicy == UploadDownloadStream {
		return fmt.Sprintf("upload [%s](%s) to [%s](%s)", t.SrcActualPath, t.Url, t.DstStorageMp, t.DstActualPath)
	}
	return fmt.Sprintf("transfer [%s](%s) to [%s](%s)", t.SrcStorageMp, t.SrcActualPath, t.DstStorageMp, t.DstActualPath)
}

func (t *TransferTask) OnSucceeded() {
	if t.DeletePolicy == DeleteOnUploadSucceed || t.DeletePolicy == DeleteAlways {
		if t.SrcStorage == nil {
			removeStdTemp(t)
		} else {
			removeObjTemp(t)
		}
	}
	task_group.TransferCoordinator.Done(context.WithoutCancel(t.Ctx()), t.groupID, true)
}

func (t *TransferTask) OnFailed() {
	if t.DeletePolicy == DeleteOnUploadFailed || t.DeletePolicy == DeleteAlways {
		if t.SrcStorage == nil {
			removeStdTemp(t)
		} else {
			removeObjTemp(t)
		}
	}
	task_group.TransferCoordinator.Done(context.WithoutCancel(t.Ctx()), t.groupID, false)
}

func (t *TransferTask) SetRetry(retry int, maxRetry int) {
	if retry == 0 &&
		(len(t.groupID) == 0 || // 重启恢复
			(t.GetErr() == nil && t.GetState() != tache.StatePending)) { // 手动重试
		t.groupID = stdpath.Join(t.DstStorageMp, t.DstActualPath)
		task_group.TransferCoordinator.AddTask(t.groupID, nil)
	}
	t.TaskData.SetRetry(retry, maxRetry)
}

var (
	TransferTaskManager *tache.Manager[*TransferTask]
)

func transferStd(ctx context.Context, tempDir, dstDirPath string, deletePolicy DeletePolicy) error {
	dstStorage, dstDirActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		return errors.WithMessage(err, "failed get dst storage")
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return err
	}
	taskCreator, _ := ctx.Value(conf.UserKey).(*model.User)
	for _, entry := range entries {
		t := &TransferTask{
			TaskData: fs.TaskData{
				TaskExtension: task.TaskExtension{
					Creator: taskCreator,
					ApiUrl:  common.GetApiUrl(ctx),
				},
				SrcActualPath: stdpath.Join(tempDir, entry.Name()),
				DstActualPath: dstDirActualPath,
				DstStorage:    dstStorage,
				DstStorageMp:  dstStorage.GetStorage().MountPath,
			},
			DeletePolicy: deletePolicy,
		}
		t.groupID = path.Join(t.DstStorageMp, t.DstActualPath)
		task_group.TransferCoordinator.AddTask(t.groupID, nil)
		TransferTaskManager.Add(t)
	}
	return nil
}

func transferStdPath(t *TransferTask) error {
	t.Status = "getting src object"
	info, err := os.Stat(t.SrcActualPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		t.Status = "src object is dir, listing objs"
		entries, err := os.ReadDir(t.SrcActualPath)
		if err != nil {
			return err
		}
		dstDirActualPath := stdpath.Join(t.DstActualPath, info.Name())
		task_group.TransferCoordinator.AppendPayload(t.groupID, task_group.DstPathToHook(dstDirActualPath))
		for _, entry := range entries {
			srcRawPath := stdpath.Join(t.SrcActualPath, entry.Name())
			task := &TransferTask{
				TaskData: fs.TaskData{
					TaskExtension: task.TaskExtension{
						Creator: t.Creator,
						ApiUrl:  t.ApiUrl,
					},
					SrcActualPath: srcRawPath,
					DstActualPath: dstDirActualPath,
					DstStorage:    t.DstStorage,
					SrcStorageMp:  t.SrcStorageMp,
					DstStorageMp:  t.DstStorageMp,
				},
				groupID:      t.groupID,
				DeletePolicy: t.DeletePolicy,
			}
			task_group.TransferCoordinator.AddTask(t.groupID, nil)
			TransferTaskManager.Add(task)
		}
		t.Status = "src object is dir, added all transfer tasks of files"
		return nil
	}
	return transferStdFile(t)
}

func transferStdFile(t *TransferTask) error {
	rc, err := os.Open(t.SrcActualPath)
	if err != nil {
		return errors.Wrapf(err, "failed to open file %s", t.SrcActualPath)
	}
	info, err := rc.Stat()
	if err != nil {
		rc.Close()
		return errors.Wrapf(err, "failed to get file %s", t.SrcActualPath)
	}

	// 尝试对天翼云进行秒传（计算 MD5 + sliceMD5）
	if rapidObj, rapidErr := tryRapidUpload189(t, rc, info.Size()); rapidErr == nil && rapidObj != nil {
		rc.Close()
		log.Infof("秒传成功: %s -> %s", t.SrcActualPath, t.DstStorageMp)
		return nil
	}

	// 秒传失败或不支持，回退到普通上传
	// 重新 seek 到文件开头
	if _, err := rc.Seek(0, 0); err != nil {
		rc.Close()
		// 重新打开文件
		rc, err = os.Open(t.SrcActualPath)
		if err != nil {
			return errors.Wrapf(err, "failed to reopen file %s", t.SrcActualPath)
		}
	}

	mimetype := utils.GetMimeType(t.SrcActualPath)
	s := &stream.FileStream{
		Ctx: t.Ctx(),
		Obj: &model.Object{
			Name:     filepath.Base(t.SrcActualPath),
			Size:     info.Size(),
			Modified: info.ModTime(),
			IsFolder: false,
		},
		Reader:   rc,
		Mimetype: mimetype,
		Closers:  utils.NewClosers(rc),
	}
	t.SetTotalBytes(info.Size())
	err = op.Put(context.WithValue(t.Ctx(), conf.SkipHookKey, struct{}{}), t.DstStorage, t.DstActualPath, s, t.SetProgress)
	if err != nil {
		return err
	}

	return nil
}

func removeStdTemp(t *TransferTask) {
	info, err := os.Stat(t.SrcActualPath)
	if err != nil || info.IsDir() {
		return
	}
	if err := os.Remove(t.SrcActualPath); err != nil {
		log.Errorf("failed to delete temp file %s, error: %s", t.SrcActualPath, err.Error())
	}
}

func transferObj(ctx context.Context, tempDir, dstDirPath string, deletePolicy DeletePolicy) error {
	srcStorage, srcObjActualPath, err := op.GetStorageAndActualPath(tempDir)
	if err != nil {
		return errors.WithMessage(err, "failed get src storage")
	}
	dstStorage, dstDirActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		return errors.WithMessage(err, "failed get dst storage")
	}
	objs, err := op.List(ctx, srcStorage, srcObjActualPath, model.ListArgs{})
	if err != nil {
		return errors.WithMessagef(err, "failed list src [%s] objs", tempDir)
	}
	taskCreator, _ := ctx.Value(conf.UserKey).(*model.User) // taskCreator is nil when convert failed
	for _, obj := range objs {
		t := &TransferTask{
			TaskData: fs.TaskData{
				TaskExtension: task.TaskExtension{
					Creator: taskCreator,
					ApiUrl:  common.GetApiUrl(ctx),
				},
				SrcActualPath: stdpath.Join(srcObjActualPath, obj.GetName()),
				DstActualPath: dstDirActualPath,
				SrcStorage:    srcStorage,
				DstStorage:    dstStorage,
				SrcStorageMp:  srcStorage.GetStorage().MountPath,
				DstStorageMp:  dstStorage.GetStorage().MountPath,
			},
			DeletePolicy: deletePolicy,
		}
		t.groupID = path.Join(t.DstStorageMp, t.DstActualPath)
		task_group.TransferCoordinator.AddTask(t.groupID, nil)
		TransferTaskManager.Add(t)
	}
	return nil
}

func transferObjPath(t *TransferTask) error {
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
		dstDirActualPath := stdpath.Join(t.DstActualPath, srcObj.GetName())
		task_group.TransferCoordinator.AppendPayload(t.groupID, task_group.DstPathToHook(dstDirActualPath))
		for _, obj := range objs {
			if utils.IsCanceled(t.Ctx()) {
				return nil
			}
			srcObjPath := stdpath.Join(t.SrcActualPath, obj.GetName())
			task_group.TransferCoordinator.AddTask(t.groupID, nil)
			TransferTaskManager.Add(&TransferTask{
				TaskData: fs.TaskData{
					TaskExtension: task.TaskExtension{
						Creator: t.Creator,
						ApiUrl:  t.ApiUrl,
					},
					SrcActualPath: srcObjPath,
					DstActualPath: dstDirActualPath,
					SrcStorage:    t.SrcStorage,
					DstStorage:    t.DstStorage,
					SrcStorageMp:  t.SrcStorageMp,
					DstStorageMp:  t.DstStorageMp,
				},
				groupID:      t.groupID,
				DeletePolicy: t.DeletePolicy,
			})
		}
		t.Status = "src object is dir, added all transfer tasks of objs"
		return nil
	}
	return transferObjFile(t)
}

func transferObjFile(t *TransferTask) error {
	_, err := op.Get(t.Ctx(), t.SrcStorage, t.SrcActualPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] file", t.SrcActualPath)
	}
	link, srcFile, err := op.Link(t.Ctx(), t.SrcStorage, t.SrcActualPath, model.LinkArgs{})
	if err != nil {
		return errors.WithMessagef(err, "failed get [%s] link", t.SrcActualPath)
	}
	// any link provided is seekable
	ss, err := stream.NewSeekableStream(&stream.FileStream{
		Obj: srcFile,
		Ctx: t.Ctx(),
	}, link)
	if err != nil {
		_ = link.Close()
		return errors.WithMessagef(err, "failed get [%s] stream", t.SrcActualPath)
	}
	t.SetTotalBytes(ss.GetSize())
	return op.Put(context.WithValue(t.Ctx(), conf.SkipHookKey, struct{}{}), t.DstStorage, t.DstActualPath, ss, t.SetProgress)
}

func removeObjTemp(t *TransferTask) {
	srcObj, err := op.Get(t.Ctx(), t.SrcStorage, t.SrcActualPath)
	if err != nil || srcObj.IsDir() {
		return
	}
	if err := op.Remove(t.Ctx(), t.SrcStorage, t.SrcActualPath); err != nil {
		log.Errorf("failed to delete temp obj %s, error: %s", t.SrcActualPath, err.Error())
	}
}

// tryRapidUpload189 尝试对天翼云进行秒传
// 通过计算文件的 MD5 来尝试秒传（使用旧版接口）
// 返回上传成功的对象和错误，如果不支持秒传则返回 nil, error
func tryRapidUpload189(t *TransferTask, file *os.File, fileSize int64) (model.Obj, error) {
	// 检查目标存储是否是天翼云 PC 驱动
	cloud189PC, ok := t.DstStorage.(*_189pc.Cloud189PC)
	if !ok {
		return nil, fmt.Errorf("not 189pc storage")
	}

	// 计算整文件 MD5（旧接口只需要 fileMD5）
	fileMD5, _, err := _189pc.ComputeSliceMD5sFromReader(file, torrent.DefaultPieceSize)
	if err != nil {
		return nil, fmt.Errorf("计算 MD5 失败: %w", err)
	}

	// 获取目标目录
	dstDir, err := op.Get(t.Ctx(), t.DstStorage, t.DstActualPath)
	if err != nil {
		return nil, fmt.Errorf("获取目标目录失败: %w", err)
	}

	// 构造文件名
	fileName := filepath.Base(t.SrcActualPath)

	// 尝试秒传（使用旧接口）
	uploadInfo, err := cloud189PC.OldUploadCreate(t.Ctx(), dstDir.GetID(), fileMD5, fileName, fmt.Sprint(fileSize), false)
	if err != nil {
		return nil, fmt.Errorf("创建上传任务失败: %w", err)
	}

	if uploadInfo.FileDataExists != 1 {
		return nil, fmt.Errorf("秒传失败：云端不存在该文件")
	}

	// 秒传成功，提交
	obj, err := cloud189PC.OldUploadCommit(t.Ctx(), uploadInfo.FileCommitUrl, uploadInfo.UploadFileId, false, true)
	if err != nil {
		return nil, fmt.Errorf("提交上传失败: %w", err)
	}

	return obj, nil
}
