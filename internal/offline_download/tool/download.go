package tool

import (
	"context"
	"fmt"
	"path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/internal/task_group"
	"github.com/OpenListTeam/tache"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const cancellationCleanupTimeout = 15 * time.Second

type DownloadTask struct {
	task.TaskExtension
	Url               string       `json:"url"`
	DstDirPath        string       `json:"dst_dir_path"`
	TempDir           string       `json:"temp_dir"`
	DeletePolicy      DeletePolicy `json:"delete_policy"`
	Toolname          string       `json:"toolname"`
	Status            string       `json:"-"`
	Signal            chan int     `json:"-"`
	GID               string       `json:"-"`
	tool              Tool
	callStatusRetried int
}

func (t *DownloadTask) Run() error {
	t.ClearEndTime()
	t.SetStartTime(time.Now())
	defer func() { t.SetEndTime(time.Now()) }()
	if err := t.checkCanceled(); err != nil {
		return err
	}
	if t.tool == nil {
		tool, err := Tools.Get(t.Toolname)
		if err != nil {
			return errors.WithMessage(err, "failed get tool")
		}
		t.tool = tool
	}
	if err := t.tool.Run(t); !errs.IsNotSupportError(err) {
		if err != nil {
			if t.Ctx().Err() != nil {
				return t.cancellationError(err)
			}
			return err
		}
		if err := t.checkCanceled(); err != nil {
			return err
		}
		transferErr := t.Transfer()
		if t.Ctx().Err() != nil {
			return t.cancellationError(transferErr)
		}
		return transferErr
	}
	if err := t.checkCanceled(); err != nil {
		return err
	}
	t.Signal = make(chan int)
	defer func() {
		t.Signal = nil
	}()
	gid, err := t.tool.AddURL(&AddUrlArgs{
		Ctx:     t.Ctx(),
		Url:     t.Url,
		UID:     t.ID,
		TempDir: t.TempDir,
		Signal:  t.Signal,
	})
	if err != nil {
		if t.Ctx().Err() != nil {
			return t.cancellationError(err)
		}
		return err
	}
	t.GID = gid
	var ok bool
outer:
	for {
		select {
		case <-t.CtxDone():
			return t.cancelDownload()
		case <-t.Signal:
			ok, err = t.Update()
			if ok {
				if t.Ctx().Err() != nil {
					return t.cancelDownload()
				}
				break outer
			}
		case <-time.After(time.Second * 3):
			ok, err = t.Update()
			if ok {
				if t.Ctx().Err() != nil {
					return t.cancelDownload()
				}
				break outer
			}
		}
	}
	if t.Ctx().Err() != nil {
		return t.cancelDownload()
	}
	if err != nil {
		return err
	}
	if t.tool.Name() == "Pikpak" {
		return t.checkCanceled()
	}
	if t.tool.Name() == "Thunder" {
		return t.checkCanceled()
	}
	if t.tool.Name() == "ThunderBrowser" {
		return t.checkCanceled()
	}
	if t.tool.Name() == "ThunderX" {
		return t.checkCanceled()
	}
	if t.tool.Name() == "GuangYaPan" {
		return t.checkCanceled()
	}
	if t.tool.Name() == "115 Cloud" {
		// hack for 115
		return t.waitAndRemove(time.Second)
	}
	if t.tool.Name() == "115 Open" {
		return t.checkCanceled()
	}
	if t.tool.Name() == "123 Open" {
		return t.checkCanceled()
	}
	t.Status = "offline download completed, maybe transferring"
	// hack for qBittorrent
	if t.tool.Name() == "qBittorrent" {
		seedTime := setting.GetInt(conf.QbittorrentSeedtime, 0)
		if seedTime >= 0 {
			t.Status = "offline download completed, waiting for seeding"
			return t.waitAndRemove(time.Minute * time.Duration(seedTime))
		}
	}

	if t.tool.Name() == "Transmission" {
		// hack for transmission
		seedTime := setting.GetInt(conf.TransmissionSeedtime, 0)
		if seedTime >= 0 {
			t.Status = "offline download completed, waiting for seeding"
			return t.waitAndRemove(time.Minute * time.Duration(seedTime))
		}
	}
	return t.checkCanceled()
}

func (t *DownloadTask) cancellationError(extra error) error {
	t.Status = "offline download canceled"
	ctxErr := t.Ctx().Err()
	if ctxErr == nil {
		ctxErr = context.Canceled
	}
	if extra == nil {
		return ctxErr
	}
	// Keep both causes discoverable with errors.Is while rendering one line in
	// the task list. errors.Join renders a newline between the causes.
	return fmt.Errorf("%w: %w", ctxErr, extra)
}

func (t *DownloadTask) checkCanceled() error {
	if t.Ctx().Err() == nil {
		return nil
	}
	return t.cancellationError(nil)
}

func (t *DownloadTask) cancelDownload() error {
	// tache v0.2.2 Worker.Execute enters onError only for a non-nil Run error;
	// onError then chooses StateCanceled from the original task context.
	return t.cancellationError(t.removeProviderTask())
}

func (t *DownloadTask) removeProviderTask() error {
	if t.GID == "" {
		return nil
	}
	cleanupParent := t.Ctx()
	if cleanupParent == nil {
		cleanupParent = context.Background()
	}
	// Preserve request values for provider authentication and keep the task's
	// canceled context intact for tache. Providers use this separate deadline
	// for the entire cleanup request sequence.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(cleanupParent), cancellationCleanupTimeout)
	defer cancel()
	return t.tool.Remove(cleanupCtx, t)
}

func (t *DownloadTask) waitAndRemove(delay time.Duration) error {
	// Cancellation during the seeding delay must wake the worker and retain
	// the canceled state, including when the timer and cancellation race.
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-t.CtxDone():
		return t.cancelDownload()
	}

	cleanupErr := t.removeProviderTask()
	if t.Ctx().Err() != nil {
		return t.cancellationError(cleanupErr)
	}
	if cleanupErr != nil {
		// Transfer has already completed. Keep successful completion and report
		// this best-effort seeding cleanup failure through the log.
		log.Errorln(cleanupErr)
	}
	return nil
}

// Update download status, return true if download completed
func (t *DownloadTask) Update() (bool, error) {
	info, err := t.tool.Status(t)
	if err != nil {
		t.callStatusRetried++
		log.Errorf("failed to get status of %s, retried %d times", t.ID, t.callStatusRetried)
		if t.callStatusRetried > 5 {
			return true, errors.Errorf("failed to get status of %s, retried %d times", t.ID, t.callStatusRetried)
		}
		return false, nil
	}
	t.callStatusRetried = 0
	t.SetProgress(info.Progress)
	t.SetTotalBytes(info.TotalBytes)
	t.Status = fmt.Sprintf("[%s]: %s", t.tool.Name(), info.Status)
	if info.NewGID != "" {
		log.Debugf("followen by: %+v", info.NewGID)
		t.GID = info.NewGID
		return false, nil
	}
	// if download completed
	if info.Completed {
		if err := t.Ctx().Err(); err != nil {
			return true, err
		}
		err := t.Transfer()
		return true, errors.WithMessage(err, "failed to transfer file")
	}
	// if download failed
	if info.Err != nil {
		return true, errors.Errorf("failed to download %s, error: %s", t.ID, info.Err.Error())
	}
	return false, nil
}

func (t *DownloadTask) Transfer() error {
	if err := t.checkCanceled(); err != nil {
		return err
	}
	toolName := t.tool.Name()
	if toolName == "115 Cloud" || toolName == "115 Open" || toolName == "123 Open" || toolName == "123Pan" || toolName == "PikPak" || toolName == "Thunder" || toolName == "ThunderX" || toolName == "ThunderBrowser" || toolName == "GuangYaPan" {
		// 如果不是直接下载到目标路径，则进行转存
		if t.TempDir != t.DstDirPath {
			return transferObj(t.Ctx(), t.TempDir, t.DstDirPath, t.DeletePolicy)
		}
		return nil
	}
	if t.DeletePolicy == UploadDownloadStream {
		dstStorage, dstDirActualPath, err := op.GetStorageAndActualPath(t.DstDirPath)
		if err != nil {
			return errors.WithMessage(err, "failed get dst storage")
		}
		taskCreator, _ := t.Ctx().Value(conf.UserKey).(*model.User)
		tsk := &TransferTask{
			TaskData: fs.TaskData{
				TaskExtension: task.TaskExtension{
					Creator: taskCreator,
					ApiUrl:  t.ApiUrl,
				},
				SrcActualPath: t.TempDir,
				DstActualPath: dstDirActualPath,
				DstStorage:    dstStorage,
				DstStorageMp:  dstStorage.GetStorage().MountPath,
			},
			DeletePolicy: t.DeletePolicy,
			Url:          t.Url,
		}
		tsk.SetTotalBytes(t.GetTotalBytes())
		tsk.groupID = path.Join(tsk.DstStorageMp, tsk.DstActualPath)
		if err := t.checkCanceled(); err != nil {
			return err
		}
		task_group.TransferCoordinator.AddTask(tsk.groupID, nil)
		TransferTaskManager.Add(tsk)
		return nil
	}
	return transferStd(t.Ctx(), t.TempDir, t.DstDirPath, t.DeletePolicy)
}

func (t *DownloadTask) GetName() string {
	return fmt.Sprintf("download %s to (%s)", t.Url, t.DstDirPath)
}

func (t *DownloadTask) GetStatus() string {
	return t.Status
}

var DownloadTaskManager *tache.Manager[*DownloadTask]
