package transcode

import (
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// Manager 全局转码管理器单例
type Manager struct {
	Registry  *WorkerRegistry
	Scheduler *Scheduler
	Cache     *Cache
	Chunks    *ChunkManager // 智能 chunk 调度器（按需启动 ffmpeg，支持随机访问）

	localRunner *LocalRunner

	stopCh chan struct{}
	once   sync.Once
}

var (
	defaultManager *Manager
	managerOnce    sync.Once
)

// Default 返回全局 Manager（懒加载）
func Default() *Manager {
	managerOnce.Do(func() {
		defaultManager = newManager()
	})
	return defaultManager
}

func newManager() *Manager {
	reg := NewWorkerRegistry()
	sch := NewScheduler(reg)
	cachePath := setting.GetStr(conf.TranscodeCachePath, "data/transcode_cache")
	cacheMaxGB := int64(setting.GetInt(conf.TranscodeCacheMaxGB, 20))
	cache := NewCache(cachePath, cacheMaxGB)
	_ = cache.Init()
	m := &Manager{
		Registry:  reg,
		Scheduler: sch,
		Cache:     cache,
		stopCh:    make(chan struct{}),
	}
	m.Chunks = NewChunkManager(m)
	return m
}

// Start 启动后台维护协程：心跳清理 + 缓存 LRU + 完成任务清理
func (m *Manager) Start() {
	m.once.Do(func() {
		go m.maintenance()
		// 根据 run_mode 决定是否启动本地 worker
		mode := setting.GetStr(conf.TranscodeRunMode, "local")
		if mode == "local" || mode == "hybrid" {
			m.startLocalWorker()
		}
	})
}

// Stop 停止 Manager
func (m *Manager) Stop() {
	close(m.stopCh)
	if m.Chunks != nil {
		m.Chunks.Stop()
	}
	if m.localRunner != nil {
		m.localRunner.Stop()
	}
}

func (m *Manager) maintenance() {
	t1 := time.NewTicker(15 * time.Second) // 心跳清理
	t2 := time.NewTicker(5 * time.Minute)  // 缓存 LRU
	t3 := time.NewTicker(30 * time.Minute) // 历史 job 清理
	t4 := time.NewTicker(10 * time.Second) // 空闲超时检测
	defer t1.Stop()
	defer t2.Stop()
	defer t3.Stop()
	defer t4.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-t1.C:
			removed := m.Registry.Sweep()
			for _, id := range removed {
				utils.Log.Infof("[transcode] worker %s removed (heartbeat timeout)", id)
			}
		case <-t2.C:
			_ = m.Cache.EnforceLRU()
		case <-t3.C:
			purged := m.Scheduler.PurgeBefore(time.Now().Add(-2 * time.Hour))
			if len(purged) > 0 {
				// 【内存泄漏修复】联动清理 Cache 中对应的 JobCache + segments map
				for _, jobID := range purged {
					m.Cache.Cleanup(jobID)
				}
				utils.Log.Infof("[transcode] purged %d finished jobs (cache cleaned)", len(purged))
			}
		case <-t4.C:
			m.sweepIdleJobs()
		}
	}
}

// sweepIdleJobs 扫描所有活跃任务，超过空闲超时时间无播放端请求则自动取消并 kill FFmpeg
func (m *Manager) sweepIdleJobs() {
	idleTimeoutSec := setting.GetInt(conf.TranscodeIdleTimeoutSec, 90)
	if idleTimeoutSec <= 0 {
		return // 配置为 0 表示禁用空闲超时
	}
	timeout := time.Duration(idleTimeoutSec) * time.Second
	now := time.Now()

	activeJobs := m.Scheduler.ListActiveJobs()
	for _, j := range activeJobs {
		lastAccess := j.GetLastAccess()
		idle := now.Sub(lastAccess)
		if idle > timeout {
			utils.Log.Infof("[transcode] job %s idle for %.0fs (threshold %ds), auto-cancelling",
				j.ID, idle.Seconds(), idleTimeoutSec)
			// 先取消所有 chunk ffmpeg，再调度器层面取消，最后清理缓存
			if m.Chunks != nil {
				m.Chunks.CancelAllForJob(j.ID)
			}
			m.Scheduler.Cancel(j.ID)
			// 清理缓存文件释放磁盘空间
			m.Cache.Cleanup(j.ID)
		}
	}
}

func (m *Manager) startLocalWorker() {
	r := NewLocalRunner(m)
	if err := r.Start(); err != nil {
		utils.Log.Errorf("[transcode] start local worker failed: %+v", err)
		return
	}
	m.localRunner = r
	// 注入取消回调：Scheduler.Cancel 时直接 kill 本地 FFmpeg 进程
	m.Scheduler.SetOnCancel(func(jobID string) {
		// 先清理 chunk session（kill 所有 chunk ffmpeg）
		if m.Chunks != nil {
			m.Chunks.CancelAllForJob(jobID)
		}
		if m.localRunner != nil {
			m.localRunner.CancelJob(jobID)
		}
	})
}

// IsEnabled 总开关
func IsEnabled() bool { return setting.GetBool(conf.TranscodeEnabled) }

// VerifyWorkerSecret 校验远程 worker 注册凭据
func VerifyWorkerSecret(token string) bool {
	expected := setting.GetStr(conf.TranscodeWorkerSecret)
	if expected == "" {
		return false // 未配置则禁用远程注册
	}
	return token == expected
}
