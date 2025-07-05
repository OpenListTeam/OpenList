package fs

import (
	"context"
	"fmt"
	stdpath "path"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/tache"
	"github.com/pkg/errors"
)

type SyncTask struct {
	task.TaskExtension
	Status         string `json:"-"`
	TaskName       string `json:"task_name"`
	SrcPath        string `json:"src_path"`
	DstPath        string `json:"dst_path"`
	TaskType       string `json:"task_type" default:"copy"`
	lazyCache      bool
	srcStorage     driver.Driver
	dstStorage     driver.Driver
	onlyInSrc      []entry
	onlyInDst      []entry
	childTasks     []task.TaskExtensionInfo
	ChildTaskInfos []model.SyncerChildTaskInfo
	mu             sync.RWMutex
}

type entry struct {
	relPath string
	obj     model.Obj
}

func (t *SyncTask) GetName() string {
	return fmt.Sprintf("%s sync [%s] to [%s] with %s mode", t.TaskName, t.SrcPath, t.DstPath, t.TaskType)
}

func (t *SyncTask) GetStatus() string {
	return t.Status
}

func (t *SyncTask) Cancel() {
	t.SetCancelFunc(func() {
		for _, childTask := range t.childTasks {
			childTask.Cancel()
		}
	})
	t.TaskExtension.Cancel()
}

func (t *SyncTask) Run() error {
	if err := t.ReinitCtx(); err != nil {
		return err
	}
	t.ClearEndTime()
	t.SetStartTime(time.Now())
	defer func() { t.SetEndTime(time.Now()) }()
	var err error
	if t.srcStorage == nil {
		t.srcStorage, err = op.GetStorageByMountPath(t.SrcPath)
	}
	if t.dstStorage == nil {
		t.dstStorage, err = op.GetStorageByMountPath(t.DstPath)
	}
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}
	return startSync(t)
}

var SyncTaskManager *tache.Manager[*SyncTask]

const maxDepth int = 10

func _syncer(ctx context.Context, syncerArgs model.SyncerTaskArgs) (task.TaskExtensionInfo, error) {
	srcPath := syncerArgs.SrcPath
	dstPath := syncerArgs.DstPath
	srcStorage, _, err := op.GetStorageAndActualPath(srcPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get src storage")
	}
	dstStorage, _, err := op.GetStorageAndActualPath(dstPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get dst storage")
	}
	taskCreator, _ := ctx.Value("user").(*model.User)
	t := &SyncTask{
		TaskExtension: task.TaskExtension{
			Creator: taskCreator,
			ApiUrl:  common.GetApiUrl(ctx),
		},
		TaskName:   syncerArgs.TaskName,
		SrcPath:    srcPath,
		DstPath:    dstPath,
		TaskType:   syncerArgs.TaskType,
		lazyCache:  syncerArgs.LazyCache,
		srcStorage: srcStorage,
		dstStorage: dstStorage,
	}
	t.SetID(strconv.Itoa(int(syncerArgs.ID)))
	SyncTaskManager.Add(t)
	return t, nil
}

func startSync(t *SyncTask) error {
	allSrcFiles, allDstFiles, err := t.getAllFile()
	if err != nil {
		return err
	}
	compare(t, allSrcFiles, allDstFiles)
	t.Status = "Handling source directory between target directory differences"
	err = t.handleSrcDiff()
	if err != nil {
		return err
	}
	err = t.handleDstDiff()
	if err != nil {
		return err
	}

	t.waitForChildTasks()
	t.Status = "Syncer finish"
	return nil
}

func (t *SyncTask) getAllFile() (map[string]model.Obj, map[string]model.Obj, error) {
	getFileFn := func(path string, lazyCache bool) (map[string]model.Obj, error) {
		result := make(map[string]model.Obj)
		walkFn := func(childPath string, info model.Obj) error {
			relPath, err := filepath.Rel(path, childPath)
			if err != nil {
				return err
			}
			result[relPath] = info
			return nil
		}
		fi, err := Get(t.Ctx(), path, &GetArgs{NoLog: true})
		if err != nil {
			return nil, err
		}
		err = WalkFSWithRefresh(t.Ctx(), maxDepth, path, fi, !lazyCache, walkFn)
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	t.Status = "getting src files"
	allSrcFiles, err := getFileFn(t.SrcPath, t.lazyCache)
	if err != nil {
		return nil, nil, err
	}
	t.Status = "getting dst files"
	allDstFiles, err := getFileFn(t.DstPath, t.lazyCache)
	if err != nil {
		return nil, nil, err
	}
	return allSrcFiles, allDstFiles, nil
}

func compare(t *SyncTask, srcFiles, dstFiles map[string]model.Obj) {
	compareFn := func(srcMap, dstMap map[string]model.Obj) []entry {
		var result []entry
		for path, obj := range srcMap {
			if _, ok := dstMap[path]; !ok {
				result = append(result, entry{path, obj})
			}
		}
		// 排序：目录优先
		sort.Slice(result, func(i, j int) bool {
			return result[i].obj.IsDir() && !result[j].obj.IsDir()
		})
		return result
	}
	t.Status = "Comparing the difference between source directory and target  directory"
	t.onlyInSrc = compareFn(srcFiles, dstFiles)
	t.onlyInDst = compareFn(dstFiles, srcFiles)
}

func (t *SyncTask) handleSrcDiff() error {
	switch t.TaskType {
	case model.Copy, model.CopyAndDelete, model.TwoWaySync:
		for _, e := range t.onlyInSrc {
			src := stdpath.Join(t.SrcPath, e.relPath)
			dst := stdpath.Join(t.DstPath, e.relPath)
			err := t.copy(e.obj, src, dst)
			if err != nil {
				return err
			}
		}
	case model.Move, model.MoveAndDelete:
		for _, e := range t.onlyInSrc {
			src := stdpath.Join(t.SrcPath, e.relPath)
			dst := stdpath.Join(t.DstPath, e.relPath)
			err := t.move(e.obj, src, dst)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *SyncTask) handleDstDiff() error {
	switch t.TaskType {
	case model.CopyAndDelete, model.MoveAndDelete, model.Delete:
		for _, e := range t.onlyInDst {
			err := t.remove(e.relPath)
			if err != nil {
				return err
			}
		}
	case model.TwoWaySync:
		for _, e := range t.onlyInDst {
			src := stdpath.Join(t.DstPath, e.relPath)
			dst := stdpath.Join(t.SrcPath, e.relPath)
			err := t.copy(e.obj, src, dst)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *SyncTask) copy(obj model.Obj, src string, dst string) error {
	if obj.IsDir() {
		err := MakeDir(t.Ctx(), dst, t.lazyCache)
		if err != nil {
			return err
		}
	} else {
		dst = filepath.Dir(dst)
		copyTask, err := Copy(t.Ctx(), src, dst)
		if err != nil {
			return err
		}
		if copyTask != nil {
			t.childTasks = append(t.childTasks, copyTask)
		} else {
			t.ChildTaskInfos = append(t.ChildTaskInfos, model.SyncerChildTaskInfo{
				TaskType: model.Copy,
				SrcPath:  src,
				DstPath:  dst,
				State:    tache.StateSucceeded,
				Progress: 100,
			})
		}
	}
	return nil
}

func (t *SyncTask) move(obj model.Obj, src string, dst string) error {
	if obj.IsDir() {
		err := MakeDir(t.Ctx(), dst, t.lazyCache)
		if err != nil {
			return err
		}
	} else {
		dst = filepath.Dir(dst)
		moveTask, err := MoveWithTaskAndValidation(t.Ctx(), src, dst, true, t.lazyCache)
		if err != nil {
			return err
		}
		if moveTask != nil {
			t.childTasks = append(t.childTasks, moveTask)
		} else {
			t.ChildTaskInfos = append(t.ChildTaskInfos, model.SyncerChildTaskInfo{
				TaskType: model.Move,
				SrcPath:  src,
				DstPath:  dst,
				State:    tache.StateSucceeded,
				Progress: 100,
			})
		}
	}
	return nil
}

func (t *SyncTask) remove(relPath string) error {
	err := Remove(t.Ctx(), stdpath.Join(t.DstPath, relPath))
	if err != nil {
		return err
	} else {
		t.ChildTaskInfos = append(t.ChildTaskInfos, model.SyncerChildTaskInfo{
			TaskType:   model.Delete,
			DeletePath: stdpath.Join(t.DstPath, relPath),
			State:      tache.StateSucceeded,
			Progress:   100,
		})
	}
	return nil
}

func (t *SyncTask) waitForChildTasks() {
	var wg sync.WaitGroup
	progressCh := make(chan struct{}, len(t.childTasks))

	total := len(t.childTasks)
	if total == 0 {
		t.SetProgress(100)
		return
	}

	for _, child := range t.childTasks {
		wg.Add(1)

		go func(task task.TaskExtensionInfo) {
			defer wg.Done()
			interval := 300 * time.Millisecond
			maxInterval := 2 * time.Second
			increaseStep := 200 * time.Millisecond

			for {
				state := task.GetState()
				t.updateOrAddChildTaskInfo(task)
				if utils.SliceContains([]tache.State{
					tache.StateSucceeded,
					tache.StateFailed,
					tache.StateCanceled,
				}, state) {
					progressCh <- struct{}{}
					return
				}
				time.Sleep(interval)
				if interval < maxInterval {
					interval += increaseStep
					if interval > maxInterval {
						interval = maxInterval
					}
				}
			}
		}(child)
	}

	go func() {
		completed := 0
		for range progressCh {
			completed++
			t.SetProgress(float64(completed) / float64(total) * 100)
			if completed == total {
				close(progressCh)
			}
		}
	}()

	wg.Wait()
}

func (t *SyncTask) updateOrAddChildTaskInfo(task task.TaskExtensionInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for i, info := range t.ChildTaskInfos {
		if info.TaskId == task.GetID() {
			t.ChildTaskInfos[i].State = task.GetState()
			return
		}
	}

	if copyTask, ok := task.(*CopyTask); ok {
		t.ChildTaskInfos = append(t.ChildTaskInfos, model.SyncerChildTaskInfo{
			TaskId:   copyTask.GetID(),
			TaskType: model.Copy,
			SrcPath:  stdpath.Join(t.srcStorage.GetStorage().MountPath, copyTask.SrcObjPath),
			DstPath:  stdpath.Join(t.dstStorage.GetStorage().MountPath, copyTask.DstDirPath),
			State:    task.GetState(),
			Progress: task.GetProgress(),
		})
		return
	}

	if moveTask, ok := task.(*MoveTask); ok {
		t.ChildTaskInfos = append(t.ChildTaskInfos, model.SyncerChildTaskInfo{
			TaskId:   moveTask.GetID(),
			TaskType: model.Move,
			SrcPath:  moveTask.SrcObjPath,
			DstPath:  moveTask.DstDirPath,
			State:    task.GetState(),
			Progress: task.GetProgress(),
		})
		return
	}

}
