package cronjob

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	// Local 驱动通过包初始化注册，供真实 fs.Copy 使用。
	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/tache"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// registerLocalStorage 注册一个真实 Local 存储。
// 使用两个不同的根目录可以保证 fs.Copy 走跨存储任务链，而不是同存储原生复制。
func registerLocalStorage(t *testing.T, root string) string {
	t.Helper()

	// 每个测试使用独立挂载路径，避免存储注册表中的路径互相影响。
	seq := syncTestStorageSeq.Add(1)
	mountPath := fmt.Sprintf("/sync-test-%d-%d", os.Getpid(), seq)
	addition, err := json.Marshal(map[string]string{
		// Local 驱动的根目录指向测试创建的真实临时目录。
		"root_folder_path": root,
	})
	if err != nil {
		t.Fatalf("failed marshal local storage addition: %+v", err)
	}

	storage := model.Storage{
		Driver:    "Local",
		MountPath: mountPath,
		Addition:  string(addition),
	}
	id, err := op.CreateStorage(context.Background(), storage)
	if err != nil {
		t.Fatalf("failed create local storage %s: %+v", mountPath, err)
	}

	// 测试结束后删除存储，防止临时目录路径泄漏到后续测试。
	t.Cleanup(func() {
		_ = op.DeleteStorageById(context.Background(), id)
	})
	return mountPath
}

// syncTestStorageSeq 生成测试存储的唯一挂载序号。
var syncTestStorageSeq atomic.Uint32

// writeTestFile 创建测试文件，并可选择性设置修改时间。
// age 过滤依赖源对象的真实修改时间，因此这里通过 os.Chtimes 模拟历史文件。
func writeTestFile(t *testing.T, root, relativePath, content string, modified ...time.Time) {
	t.Helper()

	fullPath := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("failed create parent for %s: %+v", fullPath, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed write %s: %+v", fullPath, err)
	}
	if len(modified) > 0 {
		if err := os.Chtimes(fullPath, modified[0], modified[0]); err != nil {
			t.Fatalf("failed change mtime for %s: %+v", fullPath, err)
		}
	}
}

// assertTestFile 校验目标文件内容。
// 断言文件内容比只断言存在更强，能发现复制任务写入不完整的问题。
func assertTestFile(t *testing.T, root, relativePath, want string) {
	t.Helper()

	fullPath := filepath.Join(root, filepath.FromSlash(relativePath))
	got, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("expected file %s: %+v", fullPath, err)
	}
	if string(got) != want {
		t.Fatalf("file %s content mismatch: want %q, got %q", fullPath, want, got)
	}
}

// assertTestFileAbsent 校验文件或目录没有被同步到目标端。
func assertTestFileAbsent(t *testing.T, root, relativePath string) {
	t.Helper()

	fullPath := filepath.Join(root, filepath.FromSlash(relativePath))
	if _, err := os.Stat(fullPath); err == nil {
		t.Fatalf("file %s should not exist", fullPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("failed stat %s: %+v", fullPath, err)
	}
}

// runSyncAndWait 用真实任务管理器执行一次同步。
// 这里不走 tache 的按秒轮询函数 waitForSyncTask，而是让测试快速等待编排任务终态；
// 但 SyncTask.Run、fs.Copy、CopyTaskManager、删除与比较逻辑全部保持真实路径。
func runSyncAndWait(t *testing.T, args SyncArgs) *SyncTask {
	t.Helper()

	if err := validateSyncArgs(args); err != nil {
		t.Fatalf("invalid sync args: %+v", err)
	}
	// SyncTaskManager.Add 是计划任务 Handler 的实际入口使用方式；
	// 调度器最终也是通过 tache.Manager 执行这个任务对象。
	syncTask := &SyncTask{Args: args}
	SyncTaskManager.Add(syncTask)
	SyncTaskManager.Wait()

	task, exists := SyncTaskManager.GetByID(syncTask.GetID())
	if !exists {
		t.Fatalf("sync task disappeared: %s", syncTask.GetID())
	}
	if task.GetState() != tache.StateSucceeded {
		t.Fatalf(
			"sync task failed\nstate: %v\nstatus: %s\nerror: %+v\nchildren: %d",
			task.GetState(), task.GetStatus(), task.GetErr(), task.TotalTasks,
		)
	}
	return task
}

// TestSyncNewFilesEndToEnd 覆盖最基本的“新文件复制”。
// 用户创建任务后的第一次运行通常就是目标端为空的情况。
func TestSyncNewFilesEndToEnd(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerLocalStorage(t, srcRoot)
	dstMount := registerLocalStorage(t, dstRoot)

	// 准备一个第一层文件、一个空目录和一个嵌套文件，覆盖常见目录结构。
	writeTestFile(t, srcRoot, "new.txt", "new file")
	if err := os.MkdirAll(filepath.Join(srcRoot, "empty-dir"), 0o755); err != nil {
		t.Fatalf("failed create empty source dir: %+v", err)
	}
	writeTestFile(t, srcRoot, "nested/child.txt", "nested file")

	task := runSyncAndWait(t, SyncArgs{
		Src: srcMount,
		Dst: dstMount,
	})

	assertTestFile(t, dstRoot, "new.txt", "new file")
	assertTestFile(t, dstRoot, "nested/child.txt", "nested file")
	// 同步目录时应创建空目录，保持源端结构。
	dstEmptyDir := filepath.Join(dstRoot, "empty-dir")
	if info, err := os.Stat(dstEmptyDir); err != nil || !info.IsDir() {
		t.Fatalf("empty dir %s should exist and be a directory: %+v", dstEmptyDir, err)
	}
	if task.TotalTasks != 2 {
		t.Fatalf("expected two copy tasks, got %d", task.TotalTasks)
	}
}

// TestSyncUpdateChangedFileEndToEnd 覆盖第二次运行时的文件更新。
// 默认比较方式是 size，因此第一次同步后只需改变大小即可触发覆盖。
func TestSyncUpdateChangedFileEndToEnd(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerLocalStorage(t, srcRoot)
	dstMount := registerLocalStorage(t, dstRoot)

	// 先做一次基线同步，让目标端已经存在文件。
	writeTestFile(t, srcRoot, "changed.txt", "old")
	writeTestFile(t, srcRoot, "same.txt", "same-size")
	runSyncAndWait(t, SyncArgs{Src: srcMount, Dst: dstMount})

	// changed.txt 的大小发生变化，same.txt 保持不变。
	writeTestFile(t, srcRoot, "changed.txt", "much longer new content")
	runSyncAndWait(t, SyncArgs{Src: srcMount, Dst: dstMount})

	assertTestFile(t, dstRoot, "changed.txt", "much longer new content")
	assertTestFile(t, dstRoot, "same.txt", "same-size")
}

// TestSyncIgnoreExistingEndToEnd 覆盖目标已有文件时 ignore_existing 的跳过语义。
// 这个选项的常见用途是不允许计划任务覆盖管理员手工放到目标端的内容。
func TestSyncIgnoreExistingEndToEnd(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerLocalStorage(t, srcRoot)
	dstMount := registerLocalStorage(t, dstRoot)

	writeTestFile(t, srcRoot, "protected.txt", "from source")
	writeTestFile(t, dstRoot, "protected.txt", "existing destination")

	runSyncAndWait(t, SyncArgs{
		Src:            srcMount,
		Dst:            dstMount,
		IgnoreExisting: true,
		Size:           true,
	})

	assertTestFile(t, dstRoot, "protected.txt", "existing destination")
}

// TestSyncMaxSizeEndToEnd 覆盖“文件大小大于等于 max_size 时不传输”。
// 用真实端到端测试可以同时验证过滤发生在复制任务创建之前。
func TestSyncMaxSizeEndToEnd(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerLocalStorage(t, srcRoot)
	dstMount := registerLocalStorage(t, dstRoot)

	writeTestFile(t, srcRoot, "small.txt", "abc")        // 3 字节
	writeTestFile(t, srcRoot, "large.txt", "abcdefghij") // 10 字节

	runSyncAndWait(t, SyncArgs{
		Src:     srcMount,
		Dst:     dstMount,
		MaxSize: 5,
	})

	assertTestFile(t, dstRoot, "small.txt", "abc")
	assertTestFileAbsent(t, dstRoot, "large.txt")
}

// TestSyncMinSizeEndToEnd 覆盖“文件大小小于等于 min_size 时不传输”。
func TestSyncMinSizeEndToEnd(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerLocalStorage(t, srcRoot)
	dstMount := registerLocalStorage(t, dstRoot)

	writeTestFile(t, srcRoot, "small.txt", "ab")          // 2 字节
	writeTestFile(t, srcRoot, "normal.txt", "abcdefghij") // 10 字节

	runSyncAndWait(t, SyncArgs{
		Src:     srcMount,
		Dst:     dstMount,
		MinSize: 5,
	})

	assertTestFile(t, dstRoot, "normal.txt", "abcdefghij")
	assertTestFileAbsent(t, dstRoot, "small.txt")
}

// TestSyncMaxAgeEndToEnd 覆盖“只同步最近 max_age 内修改的文件”。
// 使用一小时阈值和一个两小时前的旧文件，避免测试受毫秒级执行时间影响。
func TestSyncMaxAgeEndToEnd(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerLocalStorage(t, srcRoot)
	dstMount := registerLocalStorage(t, dstRoot)

	oldTime := time.Now().Add(-2 * time.Hour)
	writeTestFile(t, srcRoot, "old.txt", "old file", oldTime)
	writeTestFile(t, srcRoot, "new.txt", "new file", time.Now().Add(-time.Minute))

	runSyncAndWait(t, SyncArgs{
		Src:    srcMount,
		Dst:    dstMount,
		MaxAge: "1h",
	})

	assertTestFile(t, dstRoot, "new.txt", "new file")
	assertTestFileAbsent(t, dstRoot, "old.txt")
}

// TestSyncMinAgeEndToEnd 覆盖“只同步 min_age 之前的文件”。
// 典型场景是备份最近一小时还没稳定的文件之前，只处理已经冷却下来的旧文件。
func TestSyncMinAgeEndToEnd(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerLocalStorage(t, srcRoot)
	dstMount := registerLocalStorage(t, dstRoot)

	oldTime := time.Now().Add(-2 * time.Hour)
	writeTestFile(t, srcRoot, "old.txt", "old file", oldTime)
	writeTestFile(t, srcRoot, "new.txt", "new file", time.Now().Add(-time.Minute))

	runSyncAndWait(t, SyncArgs{
		Src:    srcMount,
		Dst:    dstMount,
		MinAge: "1h",
	})

	assertTestFile(t, dstRoot, "old.txt", "old file")
	assertTestFileAbsent(t, dstRoot, "new.txt")
}

// TestSyncExcludeEndToEnd 覆盖 glob 排除规则。
// 既要排除第一层文件，也要排除子目录中的同名类型文件。
func TestSyncExcludeEndToEnd(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerLocalStorage(t, srcRoot)
	dstMount := registerLocalStorage(t, dstRoot)

	writeTestFile(t, srcRoot, "keep.txt", "keep")
	writeTestFile(t, srcRoot, "skip.log", "skip root")
	writeTestFile(t, srcRoot, "logs/child.log", "skip child")
	writeTestFile(t, srcRoot, "logs/keep.txt", "keep child")

	runSyncAndWait(t, SyncArgs{
		Src:     srcMount,
		Dst:     dstMount,
		Exclude: []string{"*.log"},
	})

	assertTestFile(t, dstRoot, "keep.txt", "keep")
	assertTestFile(t, dstRoot, "logs/keep.txt", "keep child")
	assertTestFileAbsent(t, dstRoot, "skip.log")
	assertTestFileAbsent(t, dstRoot, "logs/child.log")
}

// TestSyncMaxDepthEndToEnd 覆盖 rclone 风格的最大递归深度。
// MaxDepth=1 表示只检查同步根目录本身，不进入任何子目录。
func TestSyncMaxDepthEndToEnd(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerLocalStorage(t, srcRoot)
	dstMount := registerLocalStorage(t, dstRoot)

	writeTestFile(t, srcRoot, "top.txt", "top")
	writeTestFile(t, srcRoot, "first/child.txt", "first child")
	writeTestFile(t, srcRoot, "first/deep/deeper.txt", "deeper")

	runSyncAndWait(t, SyncArgs{
		Src:      srcMount,
		Dst:      dstMount,
		MaxDepth: 1,
	})

	assertTestFile(t, dstRoot, "top.txt", "top")
	assertTestFileAbsent(t, dstRoot, "first/child.txt")
	assertTestFileAbsent(t, dstRoot, "first/deep/deeper.txt")
}

// TestSyncDeleteBeforeEndToEnd 覆盖目标端多余文件的镜像删除。
// rclone sync 与 copy 的关键差别就是必须删除目标端多余对象。
func TestSyncDeleteBeforeEndToEnd(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerLocalStorage(t, srcRoot)
	dstMount := registerLocalStorage(t, dstRoot)

	writeTestFile(t, srcRoot, "keep.txt", "current source")
	writeTestFile(t, dstRoot, "stale.txt", "old destination")

	runSyncAndWait(t, SyncArgs{Src: srcMount, Dst: dstMount})

	assertTestFile(t, dstRoot, "keep.txt", "current source")
	assertTestFileAbsent(t, dstRoot, "stale.txt")
}

// TestSyncExcludeWithDeleteBeforeEndToEnd 覆盖排除规则与镜像删除同时生效。
// rclone 的默认语义是：目标端被排除的对象即使源端不存在，也不应该被 sync 删除。
func TestSyncExcludeWithDeleteBeforeEndToEnd(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerLocalStorage(t, srcRoot)
	dstMount := registerLocalStorage(t, dstRoot)

	writeTestFile(t, srcRoot, "keep.txt", "current source")
	writeTestFile(t, dstRoot, "stale.txt", "delete this")
	writeTestFile(t, dstRoot, "excluded.log", "do not delete excluded object")

	runSyncAndWait(t, SyncArgs{
		Src:     srcMount,
		Dst:     dstMount,
		Exclude: []string{"*.log"},
	})

	assertTestFile(t, dstRoot, "keep.txt", "current source")
	assertTestFileAbsent(t, dstRoot, "stale.txt")
	assertTestFile(t, dstRoot, "excluded.log", "do not delete excluded object")
}

// TestSyncRejectsOverlappingSameStoragePaths 覆盖路径穿越/互套保护。
// 真实用户配置中如果把 src 配成 dst 的父目录，会导致源和目标互相污染。
func TestSyncRejectsOverlappingSameStoragePaths(t *testing.T) {
	root := t.TempDir()
	mount := registerLocalStorage(t, root)

	// 构造一个 Handler 实际使用到的校验入口；
	// Run 之前 validateSyncPaths 也会走同一段代码。
	handler, err := LookupHandler("sync")
	if err != nil {
		t.Fatalf("sync handler is not registered: %+v", err)
	}
	raw, err := json.Marshal(SyncArgs{
		Src: mount + "/src",
		Dst: mount + "/src/child",
	})
	if err != nil {
		t.Fatalf("failed marshal sync args: %+v", err)
	}
	if err := handler.ValidateArgs(raw); err == nil {
		t.Fatal("expected overlapping same-storage sync paths to be rejected")
	}
}

// TestCronSchedulerTickRunsExpiredJob 覆盖调度器真实 tick 到期触发逻辑。
// 不等待 30 秒 ticker，而是直接调用每次 tick 执行的 tickCronJobs。
func TestCronSchedulerTickRunsExpiredJob(t *testing.T) {
	var executed atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})

	// 注册一个测试专用 Handler：实际进入 executeJob 后立即计数，
	// 等测试观察到 running 状态后再退出，保证锁测试确定性。
	RegisterHandler("cron-test-fast", HandlerFunc{
		ValidateFunc: func(raw json.RawMessage) error { return nil },
		RunFunc: func(ctx context.Context, raw json.RawMessage) error {
			executed.Add(1)
			close(started)
			<-release
			return nil
		},
	})

	job := &model.CronJob{
		Name:     "cron-test-fast-job",
		Type:     "cron-test-fast",
		CronSpec: "* * * * *",
		Enabled:  true,
		Args:     "{}",
		// NextRunAt 已过期，tickCronJobs 应当立即启动它。
		NextRunAt: &nowMinusMinute,
	}
	if err := db.CreateCronJob(job); err != nil {
		t.Fatalf("failed create cronjob: %+v", err)
	}

	tickCronJobs(context.Background())
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("expired cronjob was not started by scheduler tick")
	}
	if !IsRunning(job.ID) {
		t.Fatal("cronjob should be running after scheduler tick")
	}

	// 让 Handler 正常结束，等待运行锁释放。
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for IsRunning(job.ID) {
		if time.Now().After(deadline) {
			t.Fatal("cronjob did not release running state")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 校验数据库运行状态：下一次运行时间已更新，且没有错误。
	updated, err := db.GetCronJobByID(job.ID)
	if err != nil {
		t.Fatalf("failed reload cronjob: %+v", err)
	}
	if updated.Running {
		t.Fatal("database running flag should be false after execution")
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("next run time should be updated, got %+v", updated.NextRunAt)
	}
	if updated.LastError != "" {
		t.Fatalf("expected no last error, got %q", updated.LastError)
	}
	if executed.Load() != 1 {
		t.Fatalf("handler executed %d times, want 1", executed.Load())
	}
}

// TestCronSchedulerRejectsRepeatedStart 覆盖同一个 CronJob 的重复执行锁。
// 定时 tick 和手动触发可能几乎同时发生，必须由同一个 running map 保护。
func TestCronSchedulerRejectsRepeatedStart(t *testing.T) {
	var executions atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})

	RegisterHandler("cron-test-blocking", HandlerFunc{
		ValidateFunc: func(raw json.RawMessage) error { return nil },
		RunFunc: func(ctx context.Context, raw json.RawMessage) error {
			executions.Add(1)
			close(started)
			<-release
			return nil
		},
	})

	job := &model.CronJob{
		Name:      "cron-test-blocking-job",
		Type:      "cron-test-blocking",
		CronSpec:  "* * * * *",
		Enabled:   true,
		Args:      "{}",
		NextRunAt: &nowMinusMinute,
	}
	if err := db.CreateCronJob(job); err != nil {
		t.Fatalf("failed create cronjob: %+v", err)
	}

	// 第一次启动应成功。
	if err := startJob(context.Background(), *job, true); err != nil {
		t.Fatalf("failed start first job: %+v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first cronjob execution did not start")
	}

	// 第二次启动必须因为 running 锁失败，且不会再次进入 Handler。
	if err := startJob(context.Background(), *job, true); err == nil {
		t.Fatal("expected second start to fail while cronjob is running")
	}
	if executions.Load() != 1 {
		t.Fatalf("handler executed %d times, want 1", executions.Load())
	}

	// 释放第一次执行并等待锁释放。
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for IsRunning(job.ID) {
		if time.Now().After(deadline) {
			t.Fatal("cronjob did not release running state")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if executions.Load() != 1 {
		t.Fatalf("handler executed %d times after release, want 1", executions.Load())
	}
}

// TestCronSpecUsedByScheduler 覆盖调度器使用的 cron 下一次计算。
// 这里选择整点前的当前分钟测试太脆弱，改为固定时间做确定性断言。
func TestCronSpecUsedByScheduler(t *testing.T) {
	spec, err := cron.ParseCronSpec("*/5 * * * *")
	if err != nil {
		t.Fatalf("failed parse scheduler cron: %+v", err)
	}
	next := spec.NextAfter(time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC))
	want := time.Date(2026, 9, 5, 1, 5, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("unexpected next time: want %s, got %s", want, next)
	}
}

// TestMain 是本包所有测试的初始化入口。
// 它建立内存数据库、Local 驱动、复制任务管理器和 sync 类型注册，
// 与真实启动流程保持一致，但不启动 30 秒调度器以避免测试间互相触发。
func TestMain(m *testing.M) {
	// 初始化与真实进程一致的配置和数据库。
	conf.Conf = conf.DefaultConfig("data")
	database, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		panic(fmt.Sprintf("failed open test database: %+v", err))
	}
	db.Init(database)

	// 预先创建 tache 的空任务数据，避免测试环境中出现“任务数据不存在”的恢复日志。
	if err := db.CreateTaskData(&model.TaskItem{Key: "cron_sync", PersistData: "[]"}); err != nil {
		panic(fmt.Sprintf("failed create test cron_sync task data: %+v", err))
	}

	// 创建管理员，供 SyncTask.Run 设置复制子任务创建者。
	if err := createMainAdmin(); err != nil {
		panic(fmt.Sprintf("failed create test admin: %+v", err))
	}

	// 初始化真实复制任务管理器；sync 的文件传输最终提交到这里。
	fs.CopyTaskManager = tache.NewManager[*fs.FileTransferTask](
		tache.WithWorks(4),
		tache.WithMaxRetry(0),
	)

	// 注册 sync Handler。RegisterSyncHandler 会先创建默认 SyncTaskManager，
	// 这里随后替换为无持久化版本，避免测试任务跨测试运行互相恢复。
	RegisterSyncHandler()
	SyncTaskManager = tache.NewManager[*SyncTask](
		tache.WithWorks(4),
		tache.WithMaxRetry(0),
	)

	os.Exit(m.Run())
}

// createMainAdmin 是 TestMain 使用的管理员创建辅助函数。
func createMainAdmin() error {
	if _, err := op.GetAdmin(); err == nil {
		return nil
	}
	salt := "cronjob-main-admin-salt"
	return op.CreateUser(&model.User{
		Username:   "cronjob-admin",
		Salt:       salt,
		PwdHash:    model.TwoHashPwd("cronjob-main-password", salt),
		Role:       model.ADMIN,
		BasePath:   "/",
		Permission: 0x71FF,
		Authn:      "[]",
	})
}

// nowMinusMinute 是调度器测试使用的过期时间。
var nowMinusMinute = time.Now().Add(-time.Minute)
