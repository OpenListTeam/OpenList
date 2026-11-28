package cronjob

import (
	"context"
	"encoding/json"
	"fmt"
	stdpath "path"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
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

	// TaskExtension 的 SetCtx 会根据 Creator/ApiUrl 注入值，因此设置之后必须重新读取，
	// 否则 fs.Copy 创建的子任务会丢失创建者（GetCreator 为 nil）。
	ctx := t.Ctx()
	if ctx == nil {
		ctx = context.Background()
	}
	t.SetCtx(ctx)
	ctx = t.Ctx()

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
		// 单文件源也必须经过排除规则；这里给它一个相对于同步根的路径，
		// 使 `*.log` 这类规则同样生效。
		if filters.excluded(srcObj.GetName()) {
			t.Status = "source excluded by filters"
			t.SetProgress(100)
			return nil
		}
		dstPath := stdpath.Join(t.Args.Dst, srcObj.GetName())
		dstObjs, err := listObjects(ctx, t.Args.Dst, true, true)
		if err != nil {
			return errors.WithMessagef(err, "failed list destination %s", t.Args.Dst)
		}
		if err := t.syncFile(ctx, filters, t.Args.Src, dstPath, dstObjs); err != nil {
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

// validateSyncPaths 拒绝源和目标落到相同或互相包含的虚拟位置。
// 只比较存储 ID 是不够的：balanced 挂载会让同一个虚拟路径轮换到不同存储，
// Alias 也可能把目标指回源数据所在的实际路径，这些情况都会让同步复制或删除到自身。
func validateSyncPaths(args SyncArgs) error {
	overlaps, err := syncPathsOverlap(args.Src, args.Dst)
	if err != nil {
		return err
	}
	if overlaps {
		return fmt.Errorf("source and destination must not overlap")
	}
	return nil
}

// syncPathsOverlap 判断两个同步路径的物理位置是否相同或互相包含。
func syncPathsOverlap(src, dst string) (bool, error) {
	srcStorage, srcActualPath, err := op.GetStorageAndActualPath(src)
	if err != nil {
		return false, errors.WithMessagef(err, "failed get source %s", src)
	}
	dstStorage, dstActualPath, err := op.GetStorageAndActualPath(dst)
	if err != nil {
		return false, errors.WithMessagef(err, "failed get destination %s", dst)
	}

	// 同一个物理存储（可能是同一个驱动实例，也可能是同一后端的不同挂载）时，
	// 直接比较实际路径是否互相包含。
	if sameBackingStore(srcStorage, dstStorage) {
		if pathsOverlap(srcActualPath, dstActualPath) {
			return true, nil
		}
	}

	// balanced 挂载下同一个虚拟路径每次解析可能落到不同存储，
	// 因此再用确定性的逻辑路径比较一次。
	srcMount, srcLogical, err := op.GetLogicalActualPath(src)
	if err != nil {
		return false, errors.WithMessagef(err, "failed resolve source %s", src)
	}
	dstMount, dstLogical, err := op.GetLogicalActualPath(dst)
	if err != nil {
		return false, errors.WithMessagef(err, "failed resolve destination %s", dst)
	}
	if srcMount == dstMount && pathsOverlap(srcLogical, dstLogical) {
		return true, nil
	}
	return false, nil
}

// sameBackingStore 判断两个驱动是否指向同一份物理数据。
func sameBackingStore(a, b driver.Driver) bool {
	if a.GetStorage().ID != 0 && a.GetStorage().ID == b.GetStorage().ID {
		return true
	}
	// 不同挂载（ID 不同）也可能指向同一后端；此时比较驱动配置。
	// Alias/URL 等驱动的 Paths 定义就是其实际数据位置。
	pa, ok := a.GetAddition().(interface{ GetPaths() string })
	if !ok {
		return false
	}
	pb, ok := b.GetAddition().(interface{ GetPaths() string })
	if !ok {
		return false
	}
	return pa.GetPaths() != "" && pa.GetPaths() == pb.GetPaths()
}

// pathsOverlap 判断 child 与 parent 是否相同或互相包含。
func pathsOverlap(a, b string) bool {
	return isPathInside(a, b) || isPathInside(b, a)
}

// syncDir 递归同步一个目录。
// extra 对象会在复制前删除（delete-before）；新增或变化的文件先删旧对象再入复制队列。
func (t *SyncTask) syncDir(ctx context.Context, filters *syncFilters, srcPath, dstPath string, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// 目录级过滤与深度限制要在列出内容之前生效，避免对被排除/超深目录发起多余请求
	// （列出虚拟挂载点等本身可能产生网络开销）。
	if depth > 0 {
		relPath := relativePath(t.Args.Src, srcPath)
		if filters.excluded(relPath) {
			return nil
		}
		if t.depthExceeded(depth) {
			return nil
		}
	}

	// Refresh 确保比较的是当前远端状态，而不是可能过期的目录缓存。
	srcObjs, err := listObjects(ctx, srcPath, true, false)
	if err != nil {
		return errors.WithMessagef(err, "failed list source %s", srcPath)
	}
	dstObjs, err := listObjects(ctx, dstPath, true, true)
	if err != nil {
		return errors.WithMessagef(err, "failed list destination %s", dstPath)
	}

	// delete-before 第一步：删除目标端多余对象。
	for name, dstObj := range dstObjs {
		if _, exists := srcObjs[name]; exists {
			continue
		}
		extraPath := stdpath.Join(dstPath, name)
		// 排除规则同样作用于目标端多余对象。
		// 例如管理员用 *.log 排除日志时，目标端不存在于源端的日志文件不应被 sync 删除。
		if filters.excluded(relativePath(t.Args.Dst, extraPath)) {
			continue
		}
		// 目标端独有的目录可能仍包含被排除/受保护的后代；必须先递归清理，
		// 只有确认目录内没有需要保留的内容后才删除目录本身。
		if dstObj.IsDir() {
			removed, err := t.removeExtraDir(ctx, filters, extraPath, depth+1)
			if err != nil {
				return err
			}
			if !removed {
				continue
			}
		}
		if err := t.removeExtraObject(ctx, extraPath); err != nil {
			return err
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
			if t.depthExceeded(nextDepth) {
				continue
			}
			// 目标同名的可能是文件（源目录替换了目标文件）；要先删除冲突文件，
			// 否则 MakeDir 会因 “file exists” 失败，导致后续同步无法反映新的目录结构。
			if dstObj, ok := dstObjs[childName]; ok && !dstObj.IsDir() {
				if t.Args.IgnoreExisting {
					continue
				}
				if err := t.removeExtraObject(ctx, childDstPath); err != nil {
					return err
				}
				delete(dstObjs, childName)
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
		if err := t.syncFile(ctx, filters, childSrcPath, childDstPath, dstObjs); err != nil {
			return err
		}
	}
	return nil
}

// syncFile 判断单个文件是否需要同步；需要时刷新计划并创建一个新的复制任务。
// dstObjs 是当前目标目录的已知列表快照，用于避免对每个文件重复列出同一目录。
func (t *SyncTask) syncFile(ctx context.Context, filters *syncFilters, srcPath, dstPath string, dstObjs map[string]model.Obj) error {
	relPath := relativePath(t.Args.Src, srcPath)
	// 读取源对象一次；大小/年龄过滤和变更比较都必须基于同一个对象快照。
	srcObj, err := getObject(ctx, srcPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get source object %s", srcPath)
	}
	// 文件名级排除在此处兜底，保证单文件源也遵循 exclude/exclude_regexp 规则。
	if filters.excluded(relPath) {
		return nil
	}
	// 大小/年龄过滤只作用于文件；目录排除已在 syncDir 中处理。
	if filters.sizeExcluded(srcObj) || filters.ageExcluded(srcObj) {
		return nil
	}

	dstObj, exists := dstObjs[stdpath.Base(dstPath)]
	if exists && t.Args.IgnoreExisting {
		return nil
	}
	// 源是文件但目标是目录时必须重建，避免"同名目录"被误判为已同步。
	if exists && dstObj.IsDir() {
		if err := t.removeExtraObject(ctx, dstPath); err != nil {
			return err
		}
		exists = false
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

	// RemoveBefore 为 true 时先删除目标同名文件：部分存储不支持覆盖上传或重命名。
	// 默认关闭，避免在传输失败时丢失目标端旧副本。
	if exists && t.Args.RemoveBefore {
		if err := t.removeExtraObject(ctx, dstPath); err != nil {
			return err
		}
		delete(dstObjs, stdpath.Base(dstPath))
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
	// 记录目标端将出现的文件，避免后续同名比较绕过目录快照。
	dstObjs[stdpath.Base(dstPath)] = srcObj
	return nil
}

// removeExtraDir 递归删除目标端独有的目录。
// 返回 false 表示目录内仍有被过滤规则保护的内容，目录本身必须保留。
func (t *SyncTask) removeExtraDir(ctx context.Context, filters *syncFilters, dirPath string, depth int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// 超出深度限制时不深入：目录内可能仍有受保护内容，保守地保留整个目录。
	if t.depthExceeded(depth) {
		return false, nil
	}
	objs, err := listObjects(ctx, dirPath, true, false)
	if err != nil {
		return false, errors.WithMessagef(err, "failed list extra directory %s", dirPath)
	}
	allRemoved := true
	for name, obj := range objs {
		childPath := stdpath.Join(dirPath, name)
		if filters.excluded(relativePath(t.Args.Dst, childPath)) {
			allRemoved = false
			continue
		}
		if obj.IsDir() {
			removed, err := t.removeExtraDir(ctx, filters, childPath, depth+1)
			if err != nil {
				return false, err
			}
			if !removed {
				allRemoved = false
				continue
			}
		}
		if err := t.removeExtraObject(ctx, childPath); err != nil {
			return false, err
		}
	}
	return allRemoved, nil
}

// removeExtraObject 删除一个目标端多余对象；删除前检查取消，避免取消后仍持续删除。
func (t *SyncTask) removeExtraObject(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.Status = fmt.Sprintf("deleting extra object %s", path)
	if err := fs.Remove(ctx, path); err != nil {
		log.Errorf("cronjob sync failed to remove extra object %s: %+v", path, err)
		return errors.WithMessagef(err, "failed remove extra destination object %s", path)
	}
	return nil
}

// depthExceeded 判断是否已经超过配置的最大递归深度。
// MaxDepth 为 0 表示不限制。
func (t *SyncTask) depthExceeded(depth int) bool {
	return t.Args.MaxDepth > 0 && depth >= t.Args.MaxDepth
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
// allowMissing 为 true 时“对象不存在”等价于空目录（用于目标目录尚未创建的场景）；
// 源目录必须传 false，避免目录消失被误判为空并从目标端删除全部内容。
func listObjects(ctx context.Context, path string, refresh bool, allowMissing bool) (map[string]model.Obj, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return nil, err
	}
	objs, err := op.List(ctx, storage, actualPath, model.ListArgs{Refresh: refresh})
	if err != nil {
		if allowMissing && errs.IsObjectNotFound(err) {
			// 目标目录不存在等价于空目录；后续 fs.Copy/MakeDir 会创建缺失目录。
			return map[string]model.Obj{}, nil
		}
		return nil, err
	}
	result := make(map[string]model.Obj, len(objs))
	for _, obj := range objs {
		result[obj.GetName()] = obj
	}
	// op.List 只返回存储本身的内容，不包含挂在虚拟目录下的子存储。
	// 这些子存储本身是虚拟目录，源端扫描必须包含它们，否则目标端同名目录会被当作多余对象删除。
	for _, obj := range op.GetStorageVirtualFilesByPath(path) {
		if _, exists := result[obj.GetName()]; !exists {
			result[obj.GetName()] = obj
		}
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
// 该函数在每次 bootstrap.Start 时都会被调用，因此这里用 sync.Once 保证
// handler 只注册一次，同时每次都重建 manager 以支持嵌入式场景下的重复启动。
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
	// 重复注册会 panic；不再重复启动时直接返回。
	registerSyncHandlerOnce.Do(func() {
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
	})
}

// registerSyncHandlerOnce 保证 sync handler 在进程内只注册一次。
var registerSyncHandlerOnce sync.Once

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
