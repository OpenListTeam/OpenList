package tool

import (
	"encoding/json"
	"fmt"
	"path"
	"sync"
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
	DstDirPath        string       `json:"dst_dir_path"`
	TempDir           string       `json:"temp_dir"`
	DeletePolicy      DeletePolicy `json:"delete_policy"`
	Toolname          string       `json:"toolname"`
	FileName          string       `json:"file_name,omitempty"`
	Status            string       `json:"-"`
	Signal            chan int     `json:"-"`
	GID               string       `json:"-"`
	tool              Tool
	callStatusRetried int
	// fileMu guards FileName and TotalBytes. The download worker writes them
	// while task APIs and persistence read them.
	fileMu sync.RWMutex
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
		Ctx:     t.Ctx(),
		Url:     t.Url,
		UID:     t.ID,
		TempDir: t.TempDir,
		Signal:  t.Signal,
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
	if t.tool.Name() == "GuangYaPan" {
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
		seedTime := setting.GetInt(conf.QbittorrentSeedtime, 0)
		if seedTime >= 0 {
			t.Status = "offline download completed, waiting for seeding"
			<-time.After(time.Minute * time.Duration(seedTime))
			err := t.tool.Remove(t)
			if err != nil {
				log.Errorln(err.Error())
			}
		}
	}

	if t.tool.Name() == "Transmission" {
		// hack for transmission
		seedTime := setting.GetInt(conf.TransmissionSeedtime, 0)
		if seedTime >= 0 {
			t.Status = "offline download completed, waiting for seeding"
			<-time.After(time.Minute * time.Duration(seedTime))
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
	// followedBy starts a different aria2 download. Drop the previous GID's
	// file name and size before persisting progress, or the metadata download
	// stays attached to the content download.
	if info.NewGID != "" {
		log.Debugf("followen by: %+v", info.NewGID)
		t.GID = info.NewGID
		t.clearFileInfo()
		t.SetProgress(info.Progress)
		t.Status = fmt.Sprintf("[%s]: %s", t.tool.Name(), info.Status)
		return false, nil
	}
	t.SetFileInfo(info.FileName, info.TotalBytes)
	t.SetProgress(info.Progress)
	t.Status = fmt.Sprintf("[%s]: %s", t.tool.Name(), info.Status)
	// if download completed
	if info.Completed {
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
		task_group.TransferCoordinator.AddTask(tsk.groupID, nil)
		TransferTaskManager.Add(tsk)
		return nil
	}
	return transferStd(t.Ctx(), t.TempDir, t.DstDirPath, t.DeletePolicy)
}

func (t *DownloadTask) GetName() string {
	return fmt.Sprintf("download %s to (%s)", t.Url, t.DstDirPath)
}

func (t *DownloadTask) SetTotalBytes(totalBytes int64) {
	t.fileMu.Lock()
	t.TotalBytes = totalBytes
	t.fileMu.Unlock()
}

func (t *DownloadTask) GetTotalBytes() int64 {
	t.fileMu.RLock()
	defer t.fileMu.RUnlock()
	return t.TotalBytes
}

func (t *DownloadTask) SetFileInfo(fileName string, fileSize int64) {
	t.fileMu.Lock()
	defer t.fileMu.Unlock()
	if fileName != "" {
		t.FileName = fileName
	}
	if fileSize > 0 {
		t.TotalBytes = fileSize
	}
}

func (t *DownloadTask) clearFileInfo() {
	t.fileMu.Lock()
	t.FileName = ""
	t.TotalBytes = 0
	t.fileMu.Unlock()
}

func (t *DownloadTask) GetFileName() string {
	t.fileMu.RLock()
	defer t.fileMu.RUnlock()
	return t.FileName
}

// GetFileSize returns the download size. Offline download tasks reuse TotalBytes
// instead of storing a second copy of the same value.
func (t *DownloadTask) GetFileSize() int64 {
	return t.GetTotalBytes()
}

// downloadTaskPersist is the on-disk form of DownloadTask.
// Add new exported DownloadTask fields here. file_size is accepted from older
// builds of this feature and folded into TotalBytes.
type downloadTaskPersist struct {
	task.TaskExtension
	Url          string       `json:"url"`
	DstDirPath   string       `json:"dst_dir_path"`
	TempDir      string       `json:"temp_dir"`
	DeletePolicy DeletePolicy `json:"delete_policy"`
	Toolname     string       `json:"toolname"`
	FileName     string       `json:"file_name,omitempty"`
	FileSize     int64        `json:"file_size,omitempty"`
}

func (t *DownloadTask) MarshalJSON() ([]byte, error) {
	t.fileMu.RLock()
	snap := downloadTaskPersist{
		TaskExtension: t.TaskExtension,
		Url:           t.Url,
		DstDirPath:    t.DstDirPath,
		TempDir:       t.TempDir,
		DeletePolicy:  t.DeletePolicy,
		Toolname:      t.Toolname,
		FileName:      t.FileName,
	}
	t.fileMu.RUnlock()
	return json.Marshal(snap)
}

func (t *DownloadTask) UnmarshalJSON(data []byte) error {
	var snap downloadTaskPersist
	if err := json.Unmarshal(data, &snap); err != nil {
		return err
	}
	if snap.TotalBytes == 0 && snap.FileSize > 0 {
		snap.TotalBytes = snap.FileSize
	}
	t.fileMu.Lock()
	t.TaskExtension = snap.TaskExtension
	t.Url = snap.Url
	t.DstDirPath = snap.DstDirPath
	t.TempDir = snap.TempDir
	t.DeletePolicy = snap.DeletePolicy
	t.Toolname = snap.Toolname
	t.FileName = snap.FileName
	t.fileMu.Unlock()
	return nil
}

func (t *DownloadTask) GetStatus() string {
	return t.Status
}

var DownloadTaskManager *tache.Manager[*DownloadTask]
