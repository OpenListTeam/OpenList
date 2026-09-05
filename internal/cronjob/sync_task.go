package cronjob

import (
	"context"
	"encoding/json"
	"fmt"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/tache"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// SyncTaskManager 管理“同步编排任务”。
// 它不是直接在 goroutine 中复制文件，而是负责扫描、筛选、排队并等待 CopyTaskManager 中的复制任务。
var SyncTaskManager *tache.Manager[*SyncTask]

// SyncTask 是一次同步执行的编排任务。
// 该任务会出现在前端任务页的 sync 分类中；具体文件传输仍出现在 copy 分类中。
type SyncTask struct {
	// TaskExtension 提供任务 ID、状态、创建者、开始/结束时间等 tache 通用字段。
	task.TaskExtension
	// Args 是本次同步的完整参数。
	Args SyncArgs `json:"args"`
	// Status 是人类可读的执行阶段描述，不参与 JSON 持久化以节省空间。
	Status string `json:"-"`
	// ChildTaskIDs 记录本任务提交给 CopyTaskManager 的所有复制任务。
	ChildTaskIDs []string `json:"child_task_ids"`
	// TotalTasks 是 ChildTaskIDs 的数量，便于前端显示进度。
	TotalTasks int `json:"total_tasks"`
	// CompletedTasks 是已经到达终态的复制任务数量。
	CompletedTasks int `json:"completed_tasks"`
}

// GetName 返回任务列表中展示的任务名。
func (t *SyncTask) GetName() string {
	return fmt.Sprintf("sync [%s] to [%s]", t.Args.Src, t.Args.Dst)
}

// GetStatus 返回任务当前阶段描述。
func (t *SyncTask) GetStatus() string {
	return t.Status
}

// Run 执行一次同步编排。
// 同步遵循 delete-before：目标多余对象、以及需要覆盖的旧文件都会先删除，再创建新的复制任务。
func (t *SyncTask) Run() error {
	if err := validateSyncArgs(t.Args); err != nil {
		return errors.WithMessage(err, "invalid sync args")
	}

	// 计划任务由管理员配置，但复制任务页需要显示创建者。
	// 这里使用系统管理员身份，避免普通用户任务列表出现 Creator 为 nil 导致的过滤问题。
	admin, err := op.GetAdmin()
	if err != nil {
		return errors.WithMessage(err, "failed get admin user")
	}
	t.Creator = admin
	if t.ApiUrl == "" {
		t.ApiUrl = conf.Conf.SiteURL
	}

	// TaskExtension 的 SetCtx 可同时注入 Creator 和 ApiUrl，供 fs.Copy 创建子任务时继承。
	ctx := t.Ctx()
	if ctx == nil {
		ctx = context.Background()
	}
	// task.TaskExtension.SetCtx 会同时注入 Creator 与 ApiUrl。
	t.SetCtx(ctx)

	t.Status = "checking sync paths"
	if err := validateSyncPaths(t.Args); err != nil {
		return err
	}

	t.Status = "preparing destination"
	// 源/目标路径语义与 rclone 相同：同步源目录的内容到目标目录，而不是把源目录名复制进目标。
	if err := fs.MakeDir(ctx, t.Args.Dst); err != nil {
		return errors.WithMessagef(err, "failed create destination %s", t.Args.Dst)
	}

	filters, err := newSyncFilters(t.Args)
	if err != nil {
		return err
	}

	srcStorage, srcActualPath, err := op.GetStorageAndActualPath(t.Args.Src)
	if err != nil {
		return errors.WithMessagef(err, "failed get source %s", t.Args.Src)
	}
	srcObj, err := op.GetUnwrap(ctx, srcStorage, srcActualPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get source object %s", t.Args.Src)
	}

	// 支持单个文件同步：此时目标是目标目录下的同名文件。
	if !srcObj.IsDir() {
		dstPath := stdpath.Join(t.Args.Dst, srcObj.GetName())
		if err := t.syncFile(ctx, filters, t.Args.Src, dstPath, 0); err != nil {
			return err
		}
		return t.waitChildTasks(ctx)
	}

	t.Status = "syncing directory"
	if err := t.syncDir(ctx, filters, t.Args.Src, t.Args.Dst, 0); err != nil {
		return err
	}
	return t.waitChildTasks(ctx)
}

// validateSyncPaths 拒绝同一个存储内源和目标互相包含的情况。
// 这种配置会导致复制任务把目标复制回自己，甚至同步删除逻辑可能影响源目录。
func validateSyncPaths(args SyncArgs) error {
	srcStorage, srcActualPath, err := op.GetStorageAndActualPath(args.Src)
	if err != nil {
		return errors.WithMessagef(err, "failed get source %s", args.Src)
	}
	dstStorage, dstActualPath, err := op.GetStorageAndActualPath(args.Dst)
	if err != nil {
		return errors.WithMessagef(err, "failed get destination %s", args.Dst)
	}
	if srcStorage.GetStorage().ID != dstStorage.GetStorage().ID {
		return nil
	}
	if isPathInside(dstActualPath, srcActualPath) || isPathInside(srcActualPath, dstActualPath) {
		return fmt.Errorf("source and destination must not overlap in the same storage")
	}
	return nil
}

// syncDir 递归同步一个目录。
// extra 对象会在复制前删除（delete-before）；新增或变化的文件先删旧对象再入复制队列。
func (t *SyncTask) syncDir(ctx context.Context, filters *syncFilters, srcPath, dstPath string, depth int) error {
	// Refresh 确保比较的是当前远端状态，而不是可能过期的目录缓存。
	srcObjs, err := listObjects(ctx, srcPath, true)
	if err != nil {
		return errors.WithMessagef(err, "failed list source %s", srcPath)
	}
	dstObjs, err := listObjects(ctx, dstPath, true)
	if err != nil {
		return errors.WithMessagef(err, "failed list destination %s", dstPath)
	}

	// delete-before 第一步：删除目标端多余对象。
	for name := range dstObjs {
		if _, exists := srcObjs[name]; exists {
			continue
		}
		extraPath := stdpath.Join(dstPath, name)
		// 排除规则同样作用于目标端多余对象。
		// 例如管理员用 *.log 排除日志时，目标端不存在于源端的日志文件不应被 sync 删除。
		if filters.excluded(relativePath(t.Args.Dst, extraPath)) {
			continue
		}
		t.Status = fmt.Sprintf("deleting extra object %s", extraPath)
		if err := fs.Remove(ctx, extraPath); err != nil {
			// 单个多余对象删除失败不阻塞其他同步项，但最终任务要标记失败。
			log.Errorf("cronjob sync failed to remove extra object %s: %+v", extraPath, err)
			return errors.WithMessagef(err, "failed remove extra destination object %s", extraPath)
		}
	}

	for _, srcObj := range srcObjs {
		if err := ctx.Err(); err != nil {
			return err
		}
		childName := srcObj.GetName()
		childSrcPath := stdpath.Join(srcPath, childName)
		childDstPath := stdpath.Join(dstPath, childName)
		childRelPath := relativePath(t.Args.Src, childSrcPath)

		// 目录和文件都可以被过滤规则排除；目录被排除后不再递归，节省 API 请求。
		if filters.excluded(childRelPath) {
			continue
		}

		if srcObj.IsDir() {
			nextDepth := depth + 1
			// MaxDepth 的计数方式和 rclone 一致：同步根目录是第 0 层。
			// MaxDepth=1 时不进入子目录；MaxDepth=2 只进入第一层子目录。
			if t.Args.MaxDepth > 0 && nextDepth >= t.Args.MaxDepth {
				continue
			}
			if err := fs.MakeDir(ctx, childDstPath); err != nil {
				return errors.WithMessagef(err, "failed create destination directory %s", childDstPath)
			}
			if err := t.syncDir(ctx, filters, childSrcPath, childDstPath, nextDepth); err != nil {
				return err
			}
			continue
		}

		// MaxDepth=1 时第一层文件仍然要同步；这里无需额外判断深度。
		if err := t.syncFile(ctx, filters, childSrcPath, childDstPath, depth); err != nil {
			return err
		}
	}
	return nil
}

// syncFile 判断单个文件是否需要同步；需要时先删除旧目标，再创建一个新的复制任务。
func (t *SyncTask) syncFile(ctx context.Context, filters *syncFilters, srcPath, dstPath string, depth int) error {
	relPath := relativePath(t.Args.Src, srcPath)
	// 读取源对象一次；大小/年龄过滤和变更比较都必须基于同一个对象快照。
	srcObj, err := getObject(ctx, srcPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get source object %s", srcPath)
	}
	// 大小/年龄过滤只作用于文件；目录排除已在 syncDir 中处理。
	if filters.sizeExcluded(srcObj) || filters.ageExcluded(srcObj) {
		return nil
	}
	// relPath/depth/filters 由 syncDir 传入；当前阶段刻意保留，方便后续增加按层级过滤。
	_, _, _ = relPath, depth, filters

	// 这里刻意使用 Refresh 重新列出父目录，避免目录列表和对象详情来自不同版本缓存。
	dstObjs, err := listObjects(ctx, stdpath.Dir(dstPath), true)
	if err != nil {
		return errors.WithMessagef(err, "failed list destination parent %s", stdpath.Dir(dstPath))
	}
	dstObj, exists := dstObjs[stdpath.Base(dstPath)]
	if exists && t.Args.IgnoreExisting {
		return nil
	}

	needSync := false
	if exists {
		needSync, err = objectsDiffer(srcObj, dstObj, t.Args)
		if err != nil {
			return errors.WithMessagef(err, "failed compare object %s", srcPath)
		}
	}
	if exists && !needSync {
		return nil
	}

	// delete-before 第二步：同名但内容不同的对象先删除，避免依赖覆盖上传语义。
	if exists {
		t.Status = fmt.Sprintf("deleting changed destination object %s", dstPath)
		if err := fs.Remove(ctx, dstPath); err != nil {
			return errors.WithMessagef(err, "failed remove changed destination object %s", dstPath)
		}
	}

	t.Status = fmt.Sprintf("queuing copy %s to %s", srcPath, dstPath)
	info, err := fs.Copy(ctx, srcPath, stdpath.Dir(dstPath))
	if err != nil {
		return errors.WithMessagef(err, "failed queue copy %s", srcPath)
	}
	// 跨存储复制会返回 CopyTaskManager 中的任务；同存储原生复制可能同步完成并返回 nil。
	if info != nil {
		t.ChildTaskIDs = append(t.ChildTaskIDs, info.GetID())
		t.TotalTasks = len(t.ChildTaskIDs)
	}
	return nil
}

// waitChildTasks 等待所有复制子任务进入终态，并更新同步任务的总进度。
func (t *SyncTask) waitChildTasks(ctx context.Context) error {
	if len(t.ChildTaskIDs) == 0 {
		t.Status = "no copy tasks queued"
		t.SetProgress(100)
		return nil
	}

	t.Status = fmt.Sprintf("waiting for copy tasks (0/%d)", t.TotalTasks)
	for {
		if err := ctx.Err(); err != nil {
			// 编排任务被取消时，也要取消还未完成的复制任务，避免后台继续传输。
			t.cancelChildTasks()
			return err
		}

		completed := 0
		failed := false
		for _, id := range t.ChildTaskIDs {
			child, ok := fs.CopyTaskManager.GetByID(id)
			if !ok {
				// 复制任务记录缺失通常发生在未持久化且进程重启后；此时无法继续等待。
				failed = true
				continue
			}
			switch child.GetState() {
			case tache.StateSucceeded, tache.StateFailed, tache.StateCanceled, tache.StateErrored:
				completed++
				if child.GetState() != tache.StateSucceeded {
					failed = true
				}
			}
		}

		t.CompletedTasks = completed
		t.Status = fmt.Sprintf("waiting for copy tasks (%d/%d)", completed, t.TotalTasks)
		t.SetProgress(float64(completed) / float64(t.TotalTasks) * 100)

		if completed >= t.TotalTasks {
			if failed {
				return fmt.Errorf("some copy tasks failed or were canceled")
			}
			t.Status = "copy tasks completed"
			t.SetProgress(100)
			return nil
		}
		time.Sleep(time.Second)
	}
}

// cancelChildTasks 取消所有尚未完成的复制任务。
func (t *SyncTask) cancelChildTasks() {
	if fs.CopyTaskManager == nil {
		return
	}
	for _, id := range t.ChildTaskIDs {
		fs.CopyTaskManager.Cancel(id)
	}
}

// listObjects 列出虚拟路径下当前真实存在的对象，并按名称建立索引。
func listObjects(ctx context.Context, path string, refresh bool) (map[string]model.Obj, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return nil, err
	}
	objs, err := op.List(ctx, storage, actualPath, model.ListArgs{Refresh: refresh})
	if err != nil {
		// 目标目录不存在等价于空目录；后续 fs.Copy/MakeDir 会创建缺失目录。
		if errs.IsObjectNotFound(err) {
			return map[string]model.Obj{}, nil
		}
		return nil, err
	}
	result := make(map[string]model.Obj, len(objs))
	for _, obj := range objs {
		result[obj.GetName()] = obj
	}
	return result, nil
}

// getObject 读取单个对象；同步逻辑需要明确的错误，不能静默返回 nil。
func getObject(ctx context.Context, path string) (model.Obj, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return nil, err
	}
	return op.GetUnwrap(ctx, storage, actualPath)
}

// objectsDiffer 判断目标对象是否需要重新复制。
func objectsDiffer(srcObj, dstObj model.Obj, args SyncArgs) (bool, error) {
	// checksum 优先：只有两侧提供相同类型的非空哈希时才可比较。
	if args.Checksum {
		same, comparable, err := hashesEqual(srcObj, dstObj)
		if err != nil {
			return false, err
		}
		if comparable {
			return !same, nil
		}
	}

	// 如果用户未选择任何比较方式，默认使用 size，避免“看起来启用了同步但什么都不复制”。
	compareSize := args.Size || (!args.Size && !args.MTime && !args.Checksum)
	if compareSize && srcObj.GetSize() != dstObj.GetSize() {
		return true, nil
	}
	if args.MTime && !srcObj.ModTime().Equal(dstObj.ModTime()) {
		return true, nil
	}
	return false, nil
}

// hashesEqual 查找两侧共同支持的哈希类型并比较。
// 第二个返回值表示是否真的找到了可比较的哈希。
func hashesEqual(srcObj, dstObj model.Obj) (bool, bool, error) {
	srcHashes := srcObj.GetHash().Export()
	for hashType, srcHash := range srcHashes {
		if hashType == nil || srcHash == "" {
			continue
		}
		dstHash := dstObj.GetHash().GetHash(hashType)
		if dstHash == "" {
			continue
		}
		return srcHash == dstHash, true, nil
	}
	return false, false, nil
}

// relativePath 计算对象相对于同步根目录的路径，供过滤规则使用。
func relativePath(root, path string) string {
	root = utils.FixAndCleanPath(root)
	path = utils.FixAndCleanPath(path)
	if utils.PathEqual(root, path) {
		return ""
	}
	if root == "/" {
		return strings.TrimPrefix(path, "/")
	}
	if strings.HasPrefix(path, root+"/") {
		return path[len(root)+1:]
	}
	return stdpath.Base(path)
}

// isPathInside 判断 child 是否等于 parent 或位于 parent 之下。
func isPathInside(child, parent string) bool {
	child = utils.FixAndCleanPath(child)
	parent = utils.FixAndCleanPath(parent)
	if child == parent {
		return true
	}
	if parent == "/" {
		return child != "/"
	}
	return strings.HasPrefix(child, parent+"/")
}

// RegisterSyncHandler 初始化同步任务管理器并注册 sync 类型。
func RegisterSyncHandler() {
	SyncTaskManager = tache.NewManager[*SyncTask](
		// 最多同时编排两条同步；具体文件上传并发仍由 CopyTaskManager 控制。
		tache.WithWorks(2),
		// 同步编排失败不自动重试，避免失败后再次创建重复复制任务。
		tache.WithMaxRetry(0),
		// 持久化 sync 编排任务，重启后能继续等待尚未完成的复制任务。
		tache.WithPersistFunction(
			getTaskDataFunc("cron_sync"),
			updateTaskDataFunc("cron_sync"),
		),
	)
	RegisterHandler("sync", DescribedHandler{
		Info: syncHandlerInfo(),
		HandlerFunc: HandlerFunc{
			ValidateFunc: func(raw json.RawMessage) error {
				var args SyncArgs
				if err := json.Unmarshal(raw, &args); err != nil {
					return fmt.Errorf("invalid sync args: %w", err)
				}
				if err := validateSyncArgs(args); err != nil {
					return err
				}
				// 创建/编辑时也要检查路径互套；不能等到定时触发时才暴露配置错误。
				return validateSyncPaths(args)
			},
			RunFunc: runSync,
		},
	})
}

// runSync 创建并等待一个 SyncTask；调度器据此保持 running 锁直到整个同步结束。
func runSync(ctx context.Context, raw json.RawMessage) error {
	if SyncTaskManager == nil {
		return fmt.Errorf("sync task manager is not initialized")
	}
	var args SyncArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return fmt.Errorf("invalid sync args: %w", err)
	}
	if err := validateSyncArgs(args); err != nil {
		return err
	}

	syncTask := &SyncTask{Args: args}
	SyncTaskManager.Add(syncTask)
	return waitForSyncTask(ctx, syncTask.GetID())
}

// waitForSyncTask 阻塞等待 SyncTask 进入终态；调用方通常是调度器后台 goroutine。
func waitForSyncTask(ctx context.Context, taskID string) error {
	for {
		select {
		case <-ctx.Done():
			// 调度器停止时取消编排任务；SyncTask 会进一步取消复制子任务。
			SyncTaskManager.Cancel(taskID)
			return ctx.Err()
		default:
		}

		syncTask, ok := SyncTaskManager.GetByID(taskID)
		if !ok {
			return fmt.Errorf("sync task %s disappeared", taskID)
		}
		switch syncTask.GetState() {
		case tache.StateSucceeded:
			return nil
		case tache.StateFailed, tache.StateErrored:
			if err := syncTask.GetErr(); err != nil {
				return err
			}
			return fmt.Errorf("sync task failed")
		case tache.StateCanceled:
			return fmt.Errorf("sync task canceled")
		}
		time.Sleep(time.Second)
	}
}
