package cronjob

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	log "github.com/sirupsen/logrus"
)

// Scheduler 管理持久化的 CronJob。
// 它只负责判断到期、加运行锁、写运行状态和执行 Handler；不解析具体任务参数。
type Scheduler struct {
	// mu 保护 running/cancels/started，防止同一个任务被定时器和手动触发同时执行。
	mu sync.Mutex
	// running 记录当前正在执行的 CronJob ID。
	running map[uint]struct{}
	// cancels 记录正在执行任务的取消函数，进程停止时可以取消上下文。
	cancels map[uint]context.CancelFunc
	// wg 等待所有正在执行的调度 goroutine 退出。
	wg sync.WaitGroup
	// started 防止重复调用 Init。
	started bool
	// scheduler 是固定周期唤醒的底层调度器。
	scheduler *cron.CronScheduler
}

// defaultScheduler 是进程内唯一的计划任务调度器。
var defaultScheduler = &Scheduler{
	running: make(map[uint]struct{}),
	cancels: make(map[uint]context.CancelFunc),
}

// InitCronJobScheduler 清理历史 running 状态并启动计划任务调度器。
// 必须在数据库初始化和所有内置 Handler 注册之后调用。
func InitCronJobScheduler() error {
	defaultScheduler.mu.Lock()
	defer defaultScheduler.mu.Unlock()
	if defaultScheduler.started {
		return fmt.Errorf("cronjob scheduler has already started")
	}

	// running 是当前进程的运行时状态；上次异常退出可能留下 true，这里统一重置。
	if err := db.ResetRunningCronJobs(); err != nil {
		return fmt.Errorf("reset running cronjobs: %w", err)
	}

	// 每 30 秒检查一次即可支持所有分钟级 cron；不需要精确到秒的调度。
	defaultScheduler.scheduler = cron.NewCronScheduler(30*time.Second, tickCronJobs)
	defaultScheduler.scheduler.Start()
	defaultScheduler.started = true
	return nil
}

// StopCronJobScheduler 停止产生新的调度 tick，并取消/等待当前正在执行的任务。
func StopCronJobScheduler() {
	defaultScheduler.mu.Lock()
	if !defaultScheduler.started || defaultScheduler.scheduler == nil {
		defaultScheduler.mu.Unlock()
		return
	}
	defaultScheduler.scheduler.Stop()

	// 取消所有执行上下文；同步 Handler 会据此取消其提交的子任务。
	for _, cancel := range defaultScheduler.cancels {
		cancel()
	}
	defaultScheduler.mu.Unlock()

	// 等待在锁外进行，避免长时间阻塞其他调度操作。
	defaultScheduler.wg.Wait()
}

// IsRunning 查询某个计划任务是否正在执行。
func IsRunning(id uint) bool {
	defaultScheduler.mu.Lock()
	defer defaultScheduler.mu.Unlock()
	_, running := defaultScheduler.running[id]
	return running
}

// RunJob 手动触发一条计划任务。它会立即返回；真正执行在后台 goroutine 中进行。
func RunJob(id uint) error {
	job, err := db.GetCronJobByID(id)
	if err != nil {
		return fmt.Errorf("get cronjob: %w", err)
	}
	// 手动触发与定时触发走同一把运行锁，确保“同一任务不能同时执行多次”。
	return startJob(context.Background(), *job, false)
}

// startJob 在获取运行锁后启动一次执行。
func startJob(parent context.Context, job model.CronJob, scheduled bool) error {
	spec, err := cron.ParseCronSpec(job.CronSpec)
	if err != nil {
		return fmt.Errorf("parse cron spec: %w", err)
	}
	handler, err := LookupHandler(job.Type)
	if err != nil {
		return err
	}

	defaultScheduler.mu.Lock()
	defer defaultScheduler.mu.Unlock()
	if _, exists := defaultScheduler.running[job.ID]; exists {
		return fmt.Errorf("cronjob %d is already running", job.ID)
	}

	now := time.Now()
	nextRunAt := job.NextRunAt
	// 定时触发后要写 next_run_at；手动触发不改变未来仍有效的计划时间。
	// 但如果 next_run_at 已过期或为空，也应当重新计算，避免恢复后重复触发。
	if scheduled || nextRunAt == nil || nextRunAt.Before(now) {
		next := spec.NextAfter(now)
		if next.IsZero() {
			return fmt.Errorf("cannot calculate next run time for cron %q", job.CronSpec)
		}
		nextRunAt = &next
	}

	lastRunAt := now
	// 在真正启动前写入状态，即使进程紧接着重启也能看到一次执行记录。
	if err := db.SetCronJobRuntime(job.ID, true, &lastRunAt, nextRunAt, ""); err != nil {
		return fmt.Errorf("update cronjob runtime: %w", err)
	}

	ctx, cancel := context.WithCancel(parent)
	defaultScheduler.running[job.ID] = struct{}{}
	defaultScheduler.cancels[job.ID] = cancel
	defaultScheduler.wg.Add(1)
	go executeJob(ctx, job, handler, &lastRunAt, nextRunAt, cancel)
	return nil
}

// executeJob 在后台执行 Handler 并回写最终状态。
func executeJob(ctx context.Context, job model.CronJob, handler Handler, lastRunAt, nextRunAt *time.Time, cancel context.CancelFunc) {
	// 无论成功、失败还是 panic，都必须释放运行锁和取消函数。
	defer func() {
		// 释放运行锁前先拿锁，保证与 startJob/StopCronJobScheduler 互斥。
		defaultScheduler.mu.Lock()
		delete(defaultScheduler.running, job.ID)
		delete(defaultScheduler.cancels, job.ID)
		cancel()
		defaultScheduler.wg.Done()
		defaultScheduler.mu.Unlock()
	}()

	// recover 可以避免某类任务配置异常导致整个进程崩溃。
	runErr := func() (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("cronjob panic: %v", recovered)
			}
		}()
		return handler.Run(ctx, []byte(job.Args))
	}()

	lastError := ""
	if runErr != nil {
		lastError = runErr.Error()
		log.Errorf("cronjob [%d/%s] failed: %+v", job.ID, job.Name, runErr)
	} else {
		log.Infof("cronjob [%d/%s] succeeded", job.ID, job.Name)
	}

	// 执行完成只回写运行状态；配置字段不会被覆盖。
	if err := db.SetCronJobRuntime(job.ID, false, lastRunAt, nextRunAt, lastError); err != nil {
		log.Errorf("update cronjob [%d] runtime after execution: %+v", job.ID, err)
	}
}

// tickCronJobs 每次定时器唤醒时检查所有启用任务是否到期。
func tickCronJobs(ctx context.Context) {
	jobs, err := db.GetCronJobs()
	if err != nil {
		log.Errorf("load cronjobs for scheduler: %+v", err)
		return
	}

	now := time.Now()
	for _, job := range jobs {
		// 先检查调度器是否已停止，避免停止过程中继续启动新任务。
		if ctx.Err() != nil {
			return
		}
		if !job.Enabled {
			continue
		}

		// cron 表达式在这里再解析一次，确保管理员更新后无需重启调度器。
		if _, err := cron.ParseCronSpec(job.CronSpec); err != nil {
			log.Errorf("cronjob [%d] has invalid cron spec %q: %+v", job.ID, job.CronSpec, err)
			continue
		}

		next := job.NextRunAt
		if next != nil && next.After(now) {
			continue
		}
		if err := startJob(ctx, job, true); err != nil {
			// 正在运行不是异常，只是为了防止上一次任务尚未结束时重复触发。
			if !IsRunning(job.ID) {
				log.Errorf("start scheduled cronjob [%d]: %+v", job.ID, err)
			}
		}
	}
}
