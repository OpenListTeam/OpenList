package handles

import (
	"errors"
	"math"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/task"

	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/tache"
	"github.com/gin-gonic/gin"
)

type TaskInfo struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Creator     string      `json:"creator"`
	CreatorRole int         `json:"creator_role"`
	State       tache.State `json:"state"`
	Status      string      `json:"status"`
	Progress    float64     `json:"progress"`
	StartTime   *time.Time  `json:"start_time"`
	EndTime     *time.Time  `json:"end_time"`
	TotalBytes  int64       `json:"total_bytes"`
	Error       string      `json:"error"`
}

type TaskPathReq struct {
	Path string `json:"path"`
}

type TaskPathResult struct {
	Matched   int `json:"matched"`
	Processed int `json:"processed"`
}

var errEmptyTaskPath = errors.New("path is required")
var errImplicitRootTaskPath = errors.New("root path must be specified explicitly as /")

// taskDoneStates are the terminal states of a task.
var taskDoneStates = []tache.State{tache.StateCanceled, tache.StateFailed, tache.StateSucceeded}

const taskDeleteWaitTimeout = 30 * time.Second

func getTaskInfo[T task.TaskExtensionInfo](task T) TaskInfo {
	errMsg := ""
	if task.GetErr() != nil {
		errMsg = task.GetErr().Error()
	}
	progress := task.GetProgress()
	// if progress is NaN, set it to 100
	if math.IsNaN(progress) {
		progress = 100
	}
	creatorName := ""
	creatorRole := -1
	if task.GetCreator() != nil {
		creatorName = task.GetCreator().Username
		creatorRole = task.GetCreator().Role
	}
	return TaskInfo{
		ID:          task.GetID(),
		Name:        task.GetName(),
		Creator:     creatorName,
		CreatorRole: creatorRole,
		State:       task.GetState(),
		Status:      task.GetStatus(),
		Progress:    progress,
		StartTime:   task.GetStartTime(),
		EndTime:     task.GetEndTime(),
		TotalBytes:  task.GetTotalBytes(),
		Error:       errMsg,
	}
}

func getTaskInfos[T task.TaskExtensionInfo](tasks []T) []TaskInfo {
	return utils.MustSliceConvert(tasks, getTaskInfo[T])
}

func argsContains[T comparable](v T, slice ...T) bool {
	return utils.SliceContains(slice, v)
}

func getUserInfo(c *gin.Context) (bool, uint, bool) {
	if user, ok := c.Request.Context().Value(conf.UserKey).(*model.User); ok {
		return user.IsAdmin(), user.ID, true
	} else {
		return false, 0, false
	}
}

func getUser(c *gin.Context) (*model.User, bool) {
	if user, ok := c.Request.Context().Value(conf.UserKey).(*model.User); ok {
		return user, true
	}
	return nil, false
}

func resolveTaskPathPrefix(user *model.User, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errEmptyTaskPath
	}
	var resolved string
	var err error
	if user.IsAdmin() {
		resolved = utils.FixAndCleanPath(raw)
	} else {
		resolved, err = user.JoinPath(raw)
	}
	if err != nil {
		return "", err
	}
	if resolved == "/" && raw != "/" {
		return "", errImplicitRootTaskPath
	}
	return resolved, nil
}

func taskOwnedBy[T task.TaskExtensionInfo](t T, isAdmin bool, uid uint) bool {
	if isAdmin {
		return true
	}
	creator := t.GetCreator()
	return creator != nil && creator.ID == uid
}

func taskMatchesPathPrefix[T task.TaskWithPaths](t T, prefix string) bool {
	return task.MatchTaskPath(t.GetSrcPath(), t.GetDstPath(), prefix)
}

func getTargetedHandler[T task.TaskExtensionInfo](manager task.Manager[T], callback func(c *gin.Context, task T)) gin.HandlerFunc {
	return func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		t, ok := manager.GetByID(c.Query("tid"))
		if !ok {
			common.ErrorStrResp(c, "task not found", 404)
			return
		}
		if !isAdmin && uid != t.GetCreator().ID {
			// to avoid an attacker using error messages to guess valid TID, return a 404 rather than a 403
			common.ErrorStrResp(c, "task not found", 404)
			return
		}
		callback(c, t)
	}
}

func getBatchHandler[T task.TaskExtensionInfo](manager task.Manager[T], callback func(task T)) gin.HandlerFunc {
	return func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		var tids []string
		if err := c.ShouldBind(&tids); err != nil {
			common.ErrorStrResp(c, "invalid request format", 400)
			return
		}
		retErrs := make(map[string]string)
		for _, tid := range tids {
			t, ok := manager.GetByID(tid)
			if !ok || (!isAdmin && uid != t.GetCreator().ID) {
				retErrs[tid] = "task not found"
				continue
			}
			callback(t)
		}
		common.SuccessResp(c, retErrs)
	}
}

type pathBatchOp[T task.TaskWithPaths] struct {
	filter func(t T) bool
	apply  func(m task.Manager[T], t T) bool
}

func matchingPathTasks[T task.TaskWithPaths](manager task.Manager[T], isAdmin bool, uid uint, prefix string, filter func(T) bool) []T {
	return manager.GetByCondition(func(t T) bool {
		return taskOwnedBy(t, isAdmin, uid) && taskMatchesPathPrefix(t, prefix) && filter(t)
	})
}

func applyPathBatch[T task.TaskWithPaths](manager task.Manager[T], isAdmin bool, uid uint, prefix string, op pathBatchOp[T]) TaskPathResult {
	tasks := matchingPathTasks(manager, isAdmin, uid, prefix, op.filter)
	result := TaskPathResult{Matched: len(tasks)}
	for _, t := range tasks {
		if op.apply(manager, t) {
			result.Processed++
		}
	}
	return result
}

func deletePathBatch[T task.TaskWithPaths](manager task.Manager[T], isAdmin bool, uid uint, prefix string) TaskPathResult {
	tasks := matchingPathTasks(manager, isAdmin, uid, prefix, func(T) bool { return true })
	result := TaskPathResult{Matched: len(tasks)}
	waitFor := make([]T, 0, len(tasks))
	pending := make(map[string]struct{}, len(tasks))

	// Cancel every matching task before waiting, so active operations stop in parallel.
	for _, selected := range tasks {
		current, ok := manager.GetByID(selected.GetID())
		if !ok {
			continue
		}
		state := current.GetState()
		if argsContains(state, taskDoneStates...) {
			continue
		}
		manager.Cancel(current.GetID())
		if current.GetStartTime() == nil && argsContains(state, tache.StatePending, tache.StateCanceling, tache.StateWaitingRetry) {
			pending[current.GetID()] = struct{}{}
		} else {
			waitFor = append(waitFor, current)
		}
	}

	deadline := time.Now().Add(taskDeleteWaitTimeout)
	for _, current := range waitFor {
		for !argsContains(current.GetState(), taskDoneStates...) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	}

	for _, selected := range tasks {
		current, ok := manager.GetByID(selected.GetID())
		if !ok {
			continue
		}
		state := current.GetState()
		// A pending task becomes canceling without running. Its canceled context
		// prevents queued execution, so it can be removed immediately. Other tasks
		// are removed only after their worker reaches a terminal state.
		_, wasPending := pending[current.GetID()]
		if !argsContains(state, taskDoneStates...) && !(wasPending && state == tache.StateCanceling) {
			continue
		}
		manager.Remove(current.GetID())
		if _, exists := manager.GetByID(current.GetID()); !exists {
			result.Processed++
		}
	}
	return result
}

func getPathBatchHandler[T task.TaskWithPaths](manager task.Manager[T], execute func(task.Manager[T], bool, uint, string) TaskPathResult) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := getUser(c)
		if !ok {
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		var req TaskPathReq
		if err := c.ShouldBind(&req); err != nil {
			common.ErrorStrResp(c, "invalid request format", 400)
			return
		}
		prefix, err := resolveTaskPathPrefix(user, req.Path)
		if err != nil {
			common.ErrorStrResp(c, err.Error(), 400)
			return
		}
		common.SuccessResp(c, execute(manager, user.IsAdmin(), user.ID, prefix))
	}
}

func pathBatchExecutor[T task.TaskWithPaths](op pathBatchOp[T]) func(task.Manager[T], bool, uint, string) TaskPathResult {
	return func(manager task.Manager[T], isAdmin bool, uid uint, prefix string) TaskPathResult {
		return applyPathBatch(manager, isAdmin, uid, prefix, op)
	}
}

func deleteByPath[T task.TaskWithPaths](manager task.Manager[T]) gin.HandlerFunc {
	return getPathBatchHandler(manager, deletePathBatch[T])
}

func cancelByPath[T task.TaskWithPaths](manager task.Manager[T]) gin.HandlerFunc {
	return getPathBatchHandler(manager, pathBatchExecutor(pathBatchOp[T]{
		filter: func(t T) bool {
			return !argsContains(t.GetState(), taskDoneStates...)
		},
		apply: func(m task.Manager[T], selected T) bool {
			current, ok := m.GetByID(selected.GetID())
			if !ok || argsContains(current.GetState(), taskDoneStates...) {
				return false
			}
			m.Cancel(current.GetID())
			return true
		},
	}))
}

func retryByPath[T task.TaskWithPaths](manager task.Manager[T]) gin.HandlerFunc {
	return getPathBatchHandler(manager, pathBatchExecutor(pathBatchOp[T]{
		filter: func(t T) bool {
			return t.GetState() == tache.StateFailed
		},
		apply: func(m task.Manager[T], selected T) bool {
			current, ok := m.GetByID(selected.GetID())
			if !ok || current.GetState() != tache.StateFailed {
				return false
			}
			m.Retry(current.GetID())
			return true
		},
	}))
}

func taskRoute[T task.TaskWithPaths](g *gin.RouterGroup, manager task.Manager[T]) {
	g.GET("/undone", func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		common.SuccessResp(c, getTaskInfos(manager.GetByCondition(func(task T) bool {
			// avoid directly passing the user object into the function to reduce closure size
			return (isAdmin || uid == task.GetCreator().ID) &&
				argsContains(task.GetState(), tache.StatePending, tache.StateRunning, tache.StateCanceling,
					tache.StateErrored, tache.StateFailing, tache.StateWaitingRetry, tache.StateBeforeRetry)
		})))
	})
	g.GET("/done", func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		common.SuccessResp(c, getTaskInfos(manager.GetByCondition(func(task T) bool {
			return (isAdmin || uid == task.GetCreator().ID) &&
				argsContains(task.GetState(), tache.StateCanceled, tache.StateFailed, tache.StateSucceeded)
		})))
	})
	g.POST("/info", getTargetedHandler(manager, func(c *gin.Context, task T) {
		common.SuccessResp(c, getTaskInfo(task))
	}))
	g.POST("/cancel", getTargetedHandler(manager, func(c *gin.Context, task T) {
		manager.Cancel(task.GetID())
		common.SuccessResp(c)
	}))
	g.POST("/delete", getTargetedHandler(manager, func(c *gin.Context, task T) {
		manager.Remove(task.GetID())
		common.SuccessResp(c)
	}))
	g.POST("/retry", getTargetedHandler(manager, func(c *gin.Context, task T) {
		manager.Retry(task.GetID())
		common.SuccessResp(c)
	}))
	g.POST("/cancel_some", getBatchHandler(manager, func(task T) {
		manager.Cancel(task.GetID())
	}))
	g.POST("/delete_some", getBatchHandler(manager, func(task T) {
		manager.Remove(task.GetID())
	}))
	g.POST("/retry_some", getBatchHandler(manager, func(task T) {
		manager.Retry(task.GetID())
	}))
	g.POST("/clear_done", func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		manager.RemoveByCondition(func(task T) bool {
			return (isAdmin || uid == task.GetCreator().ID) &&
				argsContains(task.GetState(), tache.StateCanceled, tache.StateFailed, tache.StateSucceeded)
		})
		common.SuccessResp(c)
	})
	g.POST("/clear_succeeded", func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		manager.RemoveByCondition(func(task T) bool {
			return (isAdmin || uid == task.GetCreator().ID) && task.GetState() == tache.StateSucceeded
		})
		common.SuccessResp(c)
	})
	g.POST("/retry_failed", func(c *gin.Context) {
		isAdmin, uid, ok := getUserInfo(c)
		if !ok {
			// if there is no bug, here is unreachable
			common.ErrorStrResp(c, "user invalid", 401)
			return
		}
		tasks := manager.GetByCondition(func(task T) bool {
			return (isAdmin || uid == task.GetCreator().ID) && task.GetState() == tache.StateFailed
		})
		for _, t := range tasks {
			manager.Retry(t.GetID())
		}
		common.SuccessResp(c)
	})
	g.POST("/delete_by_path", deleteByPath(manager))
	g.POST("/cancel_by_path", cancelByPath(manager))
	g.POST("/retry_by_path", retryByPath(manager))
}

func SetupTaskRoute(g *gin.RouterGroup) {
	taskRoute(g.Group("/upload"), fs.UploadTaskManager)
	taskRoute(g.Group("/copy"), fs.CopyTaskManager)
	taskRoute(g.Group("/move"), fs.MoveTaskManager)
	taskRoute(g.Group("/offline_download"), tool.DownloadTaskManager)
	taskRoute(g.Group("/offline_download_transfer"), tool.TransferTaskManager)
	taskRoute(g.Group("/decompress"), fs.ArchiveDownloadTaskManager)
	taskRoute(g.Group("/decompress_upload"), fs.ArchiveContentUploadTaskManager)
}
