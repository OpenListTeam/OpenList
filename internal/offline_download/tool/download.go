package tool

import (
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

type DownloadTask struct {
	task.TaskExtension
	Url               string       `json:"url"`
	TorrentData       []byte       `json:"-"`
	DstDirPath        string       `json:"dst_dir_path"`
	TempDir           string       `json:"temp_dir"`
	DeletePolicy      DeletePolicy `json:"delete_policy"`
	DeleteAfterTime   time.Time    `json:"delete_after_time,omitempty"`
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
	if t.tool == nil {
		tool, err := Tools.Get(t.Toolname)
		if err != nil {
			return errors.WithMessage(err, "failed get tool")
		}
		t.tool = tool
	}
	if err := t.tool.Run(t); !errs.IsNotSupportError(err) {
		if err == nil {
			return t.Transfer()
		}
		return err
	}
	t.Signal = make(chan int)
	defer func() {
		t.Signal = nil
	}()
	gid, err := t.tool.AddURL(&AddUrlArgs{
		Ctx:         t.Ctx(),
		Url:         t.Url,
		TorrentData: t.TorrentData,
		UID:         t.ID,
		TempDir:     t.TempDir,
		Signal:      t.Signal,
	})
	if err != nil {
		return err
	}
	t.GID = gid
	var ok bool
outer:
	for {
		select {
		case <-t.CtxDone():
			err := t.tool.Remove(t)
			return err
		case <-t.Signal:
			ok, err = t.Update()
			if ok {
				break outer
			}
		case <-time.After(time.Second * 3):
			ok, err = t.Update()
			if ok {
				break outer
			}
		}
	}
	if err != nil {
		return err
	}
	if t.tool.Name() == "Pikpak" {
		return nil
	}
	if t.tool.Name() == "Thunder" {
		return nil
	}
	if t.tool.Name() == "ThunderBrowser" {
		return nil
	}
	if t.tool.Name() == "ThunderX" {
		return nil
	}
	if t.tool.Name() == "115 Cloud" {
		// hack for 115
		<-time.After(time.Second * 1)
		err := t.tool.Remove(t)
		if err != nil {
			log.Errorln(err.Error())
		}
		return nil
	}
	if t.tool.Name() == "115 Open" {
		return nil
	}
	if t.tool.Name() == "123 Open" {
		return nil
	}
	t.Status = "offline download completed, maybe transferring"
	// hack for qBittorrent
	if t.tool.Name() == "qBittorrent" {
		if seedDuration, ok := t.seedingDuration(); ok {
			t.Status = "offline download completed, waiting for seeding"
			<-time.After(seedDuration)
			err := t.tool.Remove(t)
			if err != nil {
				log.Errorln(err.Error())
			}
		}
	}

	if t.tool.Name() == "Transmission" {
		// hack for transmission
		if seedDuration, ok := t.seedingDuration(); ok {
			t.Status = "offline download completed, waiting for seeding"
			<-time.After(seedDuration)
			err := t.tool.Remove(t)
			if err != nil {
				log.Errorln(err.Error())
			}
		}
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
		t.setDeleteAfterTime()
		err := t.Transfer()
		return true, errors.WithMessage(err, "failed to transfer file")
	}
	// if download failed
	if info.Err != nil {
		return true, errors.Errorf("failed to download %s, error: %s", t.ID, info.Err.Error())
	}
	return false, nil
}

func (t *DownloadTask) setDeleteAfterTime() {
	if t.DeletePolicy != DeleteAfterSeeding || !t.isSeedingTool() || !t.DeleteAfterTime.IsZero() {
		return
	}
	seedDuration, ok := t.seedingDuration()
	if !ok {
		return
	}
	t.DeleteAfterTime = time.Now().Add(seedDuration)
}

func (t *DownloadTask) transferDeletePolicy() DeletePolicy {
	if t.DeletePolicy == DeleteAfterSeeding && !t.isSeedingTool() {
		return DeleteNever
	}
	return t.DeletePolicy
}

func (t *DownloadTask) isSeedingTool() bool {
	toolName := t.tool.Name()
	return toolName == "qBittorrent" || toolName == "Transmission"
}

func (t *DownloadTask) seedingDuration() (time.Duration, bool) {
	var seedTime int
	switch t.tool.Name() {
	case "qBittorrent":
		seedTime = setting.GetInt(conf.QbittorrentSeedtime, 0)
	case "Transmission":
		seedTime = setting.GetInt(conf.TransmissionSeedtime, 0)
	default:
		return 0, false
	}
	if seedTime < 0 {
		return 0, false
	}
	return time.Minute * time.Duration(seedTime), true
}

func (t *DownloadTask) Transfer() error {
	toolName := t.tool.Name()
	deletePolicy := t.transferDeletePolicy()
	if toolName == "115 Cloud" || toolName == "115 Open" || toolName == "123 Open" || toolName == "123Pan" || toolName == "PikPak" || toolName == "Thunder" || toolName == "ThunderX" || toolName == "ThunderBrowser" {
		// 如果不是直接下载到目标路径，则进行转存
		if t.TempDir != t.DstDirPath {
			return transferObj(t.Ctx(), t.TempDir, t.DstDirPath, deletePolicy, t.DeleteAfterTime)
		}
		return nil
	}
	if deletePolicy == UploadDownloadStream {
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
			DeletePolicy:    deletePolicy,
			DeleteAfterTime: t.DeleteAfterTime,
			Url:             t.Url,
		}
		tsk.SetTotalBytes(t.GetTotalBytes())
		tsk.groupID = path.Join(tsk.DstStorageMp, tsk.DstActualPath)
		task_group.TransferCoordinator.AddTask(tsk.groupID, nil)
		TransferTaskManager.Add(tsk)
		return nil
	}
	return transferStd(t.Ctx(), t.TempDir, t.DstDirPath, deletePolicy, t.DeleteAfterTime)
}

func (t *DownloadTask) GetName() string {
	return fmt.Sprintf("download %s to (%s)", t.Url, t.DstDirPath)
}

func (t *DownloadTask) GetStatus() string {
	return t.Status
}

var DownloadTaskManager *tache.Manager[*DownloadTask]
