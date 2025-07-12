package fs

import (
	"context"
	"fmt"
	stdpath "path"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/tache"
	"github.com/pkg/errors"
)

type MoveTask struct {
	task.TaskExtension
	Status            string        `json:"-"`
	SrcObjPath        string        `json:"src_path"`
	DstDirPath        string        `json:"dst_path"`
	srcStorage        driver.Driver `json:"-"`
	dstStorage        driver.Driver `json:"-"`
	SrcStorageMp      string        `json:"src_storage_mp"`
	DstStorageMp      string        `json:"dst_storage_mp"`
	IsRootTask        bool          `json:"is_root_task"`
	RootTaskID        string        `json:"root_task_id"`
	TotalFiles        int           `json:"total_files"`
	CompletedFiles    int           `json:"completed_files"`
	Phase             string        `json:"phase"` // "copying", "verifying", "deleting", "completed"
	ValidateExistence bool          `json:"validate_existence"`
	mu                sync.RWMutex  `json:"-"`
}

type MoveProgress struct {
	TaskID         string `json:"task_id"`
	Phase          string `json:"phase"`
	TotalFiles     int    `json:"total_files"`
	CompletedFiles int    `json:"completed_files"`
	CurrentFile    string `json:"current_file"`
	Status         string `json:"status"`
	Progress       int    `json:"progress"`
}

var moveProgressMap = sync.Map{}

func (t *MoveTask) GetName() string {
	return fmt.Sprintf("move [%s](%s) to [%s](%s)", t.SrcStorageMp, t.SrcObjPath, t.DstStorageMp, t.DstDirPath)
}

func (t *MoveTask) GetStatus() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status
}

func (t *MoveTask) GetProgress() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.TotalFiles == 0 {
		return 0
	}

	switch t.Phase {
	case "copying":
		return float64(t.CompletedFiles*60) / float64(t.TotalFiles)
	case "verifying":
		return 60 + float64(t.CompletedFiles*20)/float64(t.TotalFiles)
	case "deleting":
		return 80 + float64(t.CompletedFiles*20)/float64(t.TotalFiles)
	case "completed":
		return 100
	default:
		return 0
	}
}

func (t *MoveTask) GetMoveProgress() *MoveProgress {
	t.mu.RLock()
	defer t.mu.RUnlock()

	progress := int(t.GetProgress())

	return &MoveProgress{
		TaskID:         t.GetID(),
		Phase:          t.Phase,
		TotalFiles:     t.TotalFiles,
		CompletedFiles: t.CompletedFiles,
		CurrentFile:    t.SrcObjPath,
		Status:         t.Status,
		Progress:       progress,
	}
}

func (t *MoveTask) updateProgress() {
	if t.IsRootTask {
		progress := t.GetMoveProgress()
		moveProgressMap.Store(t.GetID(), progress)
	}
}

func (t *MoveTask) Run() error {
	if err := t.ReinitCtx(); err != nil {
		return err
	}
	t.ClearEndTime()
	t.SetStartTime(time.Now())
	defer func() {
		t.SetEndTime(time.Now())
		if t.IsRootTask {
			moveProgressMap.Delete(t.GetID())
		}
	}()

	var err error
	if t.srcStorage == nil {
		t.srcStorage, err = op.GetStorageByMountPath(t.SrcStorageMp)
	}
	if t.dstStorage == nil {
		t.dstStorage, err = op.GetStorageByMountPath(t.DstStorageMp)
	}
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}

	// Phase 1: Async validation (all validation happens in background)
	t.mu.Lock()
	t.Status = "validating source and destination"
	t.mu.Unlock()

	// Check if source exists
	srcObj, err := op.Get(t.Ctx(), t.srcStorage, t.SrcObjPath)
	if err != nil {
		return errors.WithMessagef(err, "source file [%s] not found", stdpath.Base(t.SrcObjPath))
	}

	// Check if destination already exists (if validation is required)
	if t.ValidateExistence {
		dstFilePath := stdpath.Join(t.DstDirPath, srcObj.GetName())
		if res, _ := op.Get(t.Ctx(), t.dstStorage, dstFilePath); res != nil {
			return errors.Errorf("destination file [%s] already exists", srcObj.GetName())
		}
	}

	// Phase 2: Execute move operation with proper sequencing
	// Determine if we should use batch optimization for directories
	if srcObj.IsDir() {
		t.mu.Lock()
		t.IsRootTask = true
		t.RootTaskID = t.GetID()
		t.mu.Unlock()
		return t.runRootMoveTask()
	}

	// Use safe move logic for files
	return t.safeMoveOperation(srcObj)
}

func (t *MoveTask) runRootMoveTask() error {
	// First check if source is actually a directory
	// If not, fall back to regular move logic
	srcObj, err := op.Get(t.Ctx(), t.srcStorage, t.SrcObjPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] object", t.SrcObjPath)
	}

	if !srcObj.IsDir() {
		// Source is not a directory, use regular move logic
		t.mu.Lock()
		t.IsRootTask = false
		t.mu.Unlock()
		return t.safeMoveOperation(srcObj)
	}

	// Phase 1: Count total files and create directory structure
	t.mu.Lock()
	t.Phase = "preparing"
	t.Status = "counting files and preparing directory structure"
	t.mu.Unlock()
	t.updateProgress()

	totalFiles, err := t.countFilesAndCreateDirs(t.srcStorage, t.dstStorage, t.SrcObjPath, t.DstDirPath)
	if err != nil {
		return errors.WithMessage(err, "failed to prepare directory structure")
	}

	t.mu.Lock()
	t.TotalFiles = totalFiles
	t.Phase = "copying"
	t.Status = "copying files"
	t.mu.Unlock()
	t.updateProgress()

	// Phase 2: Copy all files
	err = t.copyAllFiles(t.srcStorage, t.dstStorage, t.SrcObjPath, t.DstDirPath)
	if err != nil {
		return errors.WithMessage(err, "failed to copy files")
	}

	// Phase 3: Verify directory structure
	t.mu.Lock()
	t.Phase = "verifying"
	t.Status = "verifying copied files"
	t.CompletedFiles = 0
	t.mu.Unlock()
	t.updateProgress()

	err = t.verifyDirectoryStructure(t.srcStorage, t.dstStorage, t.SrcObjPath, t.DstDirPath)
	if err != nil {
		return errors.WithMessage(err, "verification failed")
	}

	// Phase 4: Delete source files and directories
	t.mu.Lock()
	t.Phase = "deleting"
	t.Status = "deleting source files"
	t.CompletedFiles = 0
	t.mu.Unlock()
	t.updateProgress()

	err = t.deleteSourceRecursively(t.srcStorage, t.SrcObjPath)
	if err != nil {
		return errors.WithMessage(err, "failed to delete source files")
	}

	t.mu.Lock()
	t.Phase = "completed"
	t.Status = "completed"
	t.mu.Unlock()
	t.updateProgress()

	return nil
}

var MoveTaskManager *tache.Manager[*MoveTask]

// GetMoveProgress returns the progress of a move task by task ID
func GetMoveProgress(taskID string) (*MoveProgress, bool) {
	if progress, ok := moveProgressMap.Load(taskID); ok {
		return progress.(*MoveProgress), true
	}
	return nil, false
}

// GetMoveTaskProgress returns the progress of a specific move task
func GetMoveTaskProgress(task *MoveTask) *MoveProgress {
	return task.GetMoveProgress()
}

// countFilesAndCreateDirs recursively counts files and creates directory structure
func (t *MoveTask) countFilesAndCreateDirs(srcStorage, dstStorage driver.Driver, srcPath, dstPath string) (int, error) {
	srcObj, err := op.Get(t.Ctx(), srcStorage, srcPath)
	if err != nil {
		return 0, errors.WithMessagef(err, "failed get src [%s] object", srcPath)
	}

	if !srcObj.IsDir() {
		return 1, nil
	}

	// Create destination directory
	dstObjPath := stdpath.Join(dstPath, srcObj.GetName())
	err = op.MakeDir(t.Ctx(), dstStorage, dstObjPath)
	if err != nil {
		if errors.Is(err, errs.UploadNotSupported) {
			return 0, errors.WithMessagef(err, "destination storage [%s] does not support creating directories", dstStorage.GetStorage().MountPath)
		}
		return 0, errors.WithMessagef(err, "failed to create destination directory [%s] in storage [%s]", dstObjPath, dstStorage.GetStorage().MountPath)
	}

	// List and count files recursively
	objs, err := op.List(t.Ctx(), srcStorage, srcPath, model.ListArgs{})
	if err != nil {
		return 0, errors.WithMessagef(err, "failed list src [%s] objs", srcPath)
	}

	totalFiles := 0
	for _, obj := range objs {
		if utils.IsCanceled(t.Ctx()) {
			return 0, nil
		}
		srcSubPath := stdpath.Join(srcPath, obj.GetName())
		subCount, err := t.countFilesAndCreateDirs(srcStorage, dstStorage, srcSubPath, dstObjPath)
		if err != nil {
			return 0, err
		}
		totalFiles += subCount
	}

	return totalFiles, nil
}

// copyAllFiles recursively copies all files
func (t *MoveTask) copyAllFiles(srcStorage, dstStorage driver.Driver, srcPath, dstPath string) error {
	srcObj, err := op.Get(t.Ctx(), srcStorage, srcPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] object", srcPath)
	}

	if !srcObj.IsDir() {
		// Copy single file
		err := t.copyFile(srcStorage, dstStorage, srcPath, dstPath)
		if err != nil {
			return err
		}

		t.mu.Lock()
		t.CompletedFiles++
		t.mu.Unlock()
		t.updateProgress()
		return nil
	}

	// Copy directory contents
	objs, err := op.List(t.Ctx(), srcStorage, srcPath, model.ListArgs{})
	if err != nil {
		return errors.WithMessagef(err, "failed list src [%s] objs", srcPath)
	}

	dstObjPath := stdpath.Join(dstPath, srcObj.GetName())
	for _, obj := range objs {
		if utils.IsCanceled(t.Ctx()) {
			return nil
		}
		srcSubPath := stdpath.Join(srcPath, obj.GetName())
		err := t.copyAllFiles(srcStorage, dstStorage, srcSubPath, dstObjPath)
		if err != nil {
			return err
		}
	}

	return nil
}

// copyFile copies a single file between storages
func (t *MoveTask) copyFile(srcStorage, dstStorage driver.Driver, srcFilePath, dstDirPath string) error {
	srcFile, err := op.Get(t.Ctx(), srcStorage, srcFilePath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] file", srcFilePath)
	}

	link, _, err := op.Link(t.Ctx(), srcStorage, srcFilePath, model.LinkArgs{})
	if err != nil {
		return errors.WithMessagef(err, "failed get [%s] link", srcFilePath)
	}
	ss, err := stream.NewSeekableStream(&stream.FileStream{
		Obj: srcFile,
		Ctx: t.Ctx(),
	}, link)
	if err != nil {
		_ = link.Close()
		return errors.WithMessagef(err, "failed get [%s] stream", srcFilePath)
	}
	return op.Put(t.Ctx(), dstStorage, dstDirPath, ss, nil, true)
}

// verifyDirectoryStructure compares source and destination directory structures
func (t *MoveTask) verifyDirectoryStructure(srcStorage, dstStorage driver.Driver, srcPath, dstPath string) error {
	srcObj, err := op.Get(t.Ctx(), srcStorage, srcPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] object", srcPath)
	}

	if !srcObj.IsDir() {
		// Verify single file
		dstFilePath := stdpath.Join(dstPath, srcObj.GetName())
		_, err := op.Get(t.Ctx(), dstStorage, dstFilePath)
		if err != nil {
			return errors.WithMessagef(err, "verification failed: destination file [%s] not found", dstFilePath)
		}

		t.mu.Lock()
		t.CompletedFiles++
		t.mu.Unlock()
		t.updateProgress()
		return nil
	}

	// Verify directory
	dstObjPath := stdpath.Join(dstPath, srcObj.GetName())
	_, err = op.Get(t.Ctx(), dstStorage, dstObjPath)
	if err != nil {
		return errors.WithMessagef(err, "verification failed: destination directory [%s] not found", dstObjPath)
	}

	// Verify directory contents
	srcObjs, err := op.List(t.Ctx(), srcStorage, srcPath, model.ListArgs{})
	if err != nil {
		return errors.WithMessagef(err, "failed list src [%s] objs for verification", srcPath)
	}

	for _, obj := range srcObjs {
		if utils.IsCanceled(t.Ctx()) {
			return nil
		}
		srcSubPath := stdpath.Join(srcPath, obj.GetName())
		err := t.verifyDirectoryStructure(srcStorage, dstStorage, srcSubPath, dstObjPath)
		if err != nil {
			return err
		}
	}

	return nil
}

// deleteSourceRecursively deletes source files and directories recursively
func (t *MoveTask) deleteSourceRecursively(srcStorage driver.Driver, srcPath string) error {
	srcObj, err := op.Get(t.Ctx(), srcStorage, srcPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] object for deletion", srcPath)
	}

	if !srcObj.IsDir() {
		// Delete single file
		err := op.Remove(t.Ctx(), srcStorage, srcPath)
		if err != nil {
			return errors.WithMessagef(err, "failed to delete src [%s] file", srcPath)
		}

		t.mu.Lock()
		t.CompletedFiles++
		t.mu.Unlock()
		t.updateProgress()
		return nil
	}

	// Delete directory contents first
	objs, err := op.List(t.Ctx(), srcStorage, srcPath, model.ListArgs{})
	if err != nil {
		return errors.WithMessagef(err, "failed list src [%s] objs for deletion", srcPath)
	}

	for _, obj := range objs {
		if utils.IsCanceled(t.Ctx()) {
			return nil
		}
		srcSubPath := stdpath.Join(srcPath, obj.GetName())
		err := t.deleteSourceRecursively(srcStorage, srcSubPath)
		if err != nil {
			return err
		}
	}

	// Delete the directory itself
	err = op.Remove(t.Ctx(), srcStorage, srcPath)
	if err != nil {
		return errors.WithMessagef(err, "failed to delete src [%s] directory", srcPath)
	}

	return nil
}

func moveBetween2Storages(t *MoveTask, srcStorage, dstStorage driver.Driver, srcObjPath, dstDirPath string) error {
	t.Status = "getting src object"
	srcObj, err := op.Get(t.Ctx(), srcStorage, srcObjPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] file", srcObjPath)
	}

	if srcObj.IsDir() {
		t.Status = "src object is dir, listing objs"
		objs, err := op.List(t.Ctx(), srcStorage, srcObjPath, model.ListArgs{})
		if err != nil {
			return errors.WithMessagef(err, "failed list src [%s] objs", srcObjPath)
		}

		dstObjPath := stdpath.Join(dstDirPath, srcObj.GetName())
		t.Status = "creating destination directory"
		err = op.MakeDir(t.Ctx(), dstStorage, dstObjPath)
		if err != nil {
			// Check if this is an upload-related error and provide a clearer message
			if errors.Is(err, errs.UploadNotSupported) {
				return errors.WithMessagef(err, "destination storage [%s] does not support creating directories", dstStorage.GetStorage().MountPath)
			}
			return errors.WithMessagef(err, "failed to create destination directory [%s] in storage [%s]", dstObjPath, dstStorage.GetStorage().MountPath)
		}

		for _, obj := range objs {
			if utils.IsCanceled(t.Ctx()) {
				return nil
			}
			srcSubObjPath := stdpath.Join(srcObjPath, obj.GetName())
			MoveTaskManager.Add(&MoveTask{
				TaskExtension: task.TaskExtension{
					Creator: t.GetCreator(),
					ApiUrl:  t.ApiUrl,
				},
				srcStorage:   srcStorage,
				dstStorage:   dstStorage,
				SrcObjPath:   srcSubObjPath,
				DstDirPath:   dstObjPath,
				SrcStorageMp: srcStorage.GetStorage().MountPath,
				DstStorageMp: dstStorage.GetStorage().MountPath,
			})
		}

		t.Status = "cleaning up source directory"
		err = op.Remove(t.Ctx(), srcStorage, srcObjPath)
		if err != nil {
			t.Status = "completed (source directory cleanup pending)"
		} else {
			t.Status = "completed"
		}
		return nil
	} else {
		return moveFileBetween2Storages(t, srcStorage, dstStorage, srcObjPath, dstDirPath)
	}
}

func moveFileBetween2Storages(tsk *MoveTask, srcStorage, dstStorage driver.Driver, srcFilePath, dstDirPath string) error {
	tsk.Status = "copying file to destination"

	copyTask := &CopyTask{
		TaskExtension: task.TaskExtension{
			Creator: tsk.GetCreator(),
			ApiUrl:  tsk.ApiUrl,
		},
		srcStorage:   srcStorage,
		dstStorage:   dstStorage,
		SrcObjPath:   srcFilePath,
		DstDirPath:   dstDirPath,
		SrcStorageMp: srcStorage.GetStorage().MountPath,
		DstStorageMp: dstStorage.GetStorage().MountPath,
	}

	copyTask.SetCtx(tsk.Ctx())

	err := copyBetween2Storages(copyTask, srcStorage, dstStorage, srcFilePath, dstDirPath)
	if err != nil {
		// Check if this is an upload-related error and provide a clearer message
		if errors.Is(err, errs.UploadNotSupported) {
			return errors.WithMessagef(err, "destination storage [%s] does not support file uploads", dstStorage.GetStorage().MountPath)
		}
		return errors.WithMessagef(err, "failed to copy [%s] to destination storage [%s]", srcFilePath, dstStorage.GetStorage().MountPath)
	}

	tsk.SetProgress(50)

	tsk.Status = "deleting source file"
	err = op.Remove(tsk.Ctx(), srcStorage, srcFilePath)
	if err != nil {
		return errors.WithMessagef(err, "failed to delete src [%s] file from storage [%s] after successful copy", srcFilePath, srcStorage.GetStorage().MountPath)
	}

	tsk.SetProgress(100)
	tsk.Status = "completed"
	return nil
}

// safeMoveOperation ensures copy-then-delete sequence for safe move operations
func (t *MoveTask) safeMoveOperation(srcObj model.Obj) error {
	if srcObj.IsDir() {
		// For directories, use the original logic but ensure proper sequencing
		return moveBetween2Storages(t, t.srcStorage, t.dstStorage, t.SrcObjPath, t.DstDirPath)
	} else {
		// For files, use the safe file move logic
		return moveFileBetween2Storages(t, t.srcStorage, t.dstStorage, t.SrcObjPath, t.DstDirPath)
	}
}

func _move(ctx context.Context, srcObjPath, dstDirPath string, lazyCache ...bool) (task.TaskExtensionInfo, error) {
	return _moveWithValidation(ctx, srcObjPath, dstDirPath, false, lazyCache...)
}

func _moveWithValidation(ctx context.Context, srcObjPath, dstDirPath string, validateExistence bool, lazyCache ...bool) (task.TaskExtensionInfo, error) {
	srcStorage, srcObjActualPath, err := op.GetStorageAndActualPath(srcObjPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get src storage")
	}
	dstStorage, dstDirActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get dst storage")
	}

	// Try native move first if in the same storage
	if srcStorage.GetStorage() == dstStorage.GetStorage() {
		err = op.Move(ctx, srcStorage, srcObjActualPath, dstDirActualPath, lazyCache...)
		if !errors.Is(err, errs.NotImplement) && !errors.Is(err, errs.NotSupport) {
			return nil, err
		}
	}

	taskCreator, _ := ctx.Value(conf.UserKey).(*model.User)

	// Create task immediately without any synchronous checks to avoid blocking frontend
	// All validation and type checking will be done asynchronously in the Run method
	t := &MoveTask{
		TaskExtension: task.TaskExtension{
			Creator: taskCreator,
			ApiUrl:  common.GetApiUrl(ctx),
		},
		srcStorage:        srcStorage,
		dstStorage:        dstStorage,
		SrcObjPath:        srcObjActualPath,
		DstDirPath:        dstDirActualPath,
		SrcStorageMp:      srcStorage.GetStorage().MountPath,
		DstStorageMp:      dstStorage.GetStorage().MountPath,
		ValidateExistence: validateExistence,
		Phase:             "initializing",
	}

	MoveTaskManager.Add(t)
	return t, nil
}
