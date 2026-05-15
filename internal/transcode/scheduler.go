package transcode

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Scheduler 任务调度器：维护 pending 队列并支持 worker pull 领取
type Scheduler struct {
	mu      sync.Mutex
	jobs    map[string]*Job // 全部任务（含完成）
	pending []*Job          // 待领取队列

	// 等待新任务的 worker 通知
	waiters []chan struct{}

	// cancellation 待派发给 worker 的取消列表（worker_id -> job_ids）
	pendingCancels map[string][]string

	// onCancel 回调：Cancel 时同步通知本地 runner kill FFmpeg 进程
	// 由 Manager 在启动时注入
	onCancel func(jobID string)

	registry *WorkerRegistry
}

func NewScheduler(reg *WorkerRegistry) *Scheduler {
	return &Scheduler{
		jobs:           make(map[string]*Job),
		pendingCancels: make(map[string][]string),
		registry:       reg,
	}
}

// SetOnCancel 注入取消回调（由 Manager 在启动本地 worker 后调用）
func (s *Scheduler) SetOnCancel(fn func(jobID string)) {
	s.mu.Lock()
	s.onCancel = fn
	s.mu.Unlock()
}

// Submit 提交一个任务并返回（带 ID + token）
func (s *Scheduler) Submit(j *Job) *Job {
	if j.ID == "" {
		j.ID = "job_" + uuid.NewString()
	}
	if j.CallbackToken == "" {
		j.CallbackToken = "tk_" + uuid.NewString()
	}
	if j.readyCh == nil {
		j.readyCh = make(chan struct{})
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now()
	}
	j.Status = JobPending

	s.mu.Lock()
	s.jobs[j.ID] = j
	s.pending = append(s.pending, j)
	// 唤醒等待的 worker
	for _, ch := range s.waiters {
		close(ch)
	}
	s.waiters = nil
	s.mu.Unlock()
	return j
}

// Get 通过 id 获取任务
func (s *Scheduler) Get(id string) (*Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok
}

// Claim 由 worker 调用领取任务（最多 slots 个）；如果没有任务则等待 wait 秒
func (s *Scheduler) Claim(workerID string, slots int, wait time.Duration) []*Job {
	worker, ok := s.registry.Get(workerID)
	if !ok {
		return nil
	}
	taken := s.claimNow(worker, slots)
	if len(taken) > 0 || wait <= 0 {
		return taken
	}
	// 长轮询等待
	s.mu.Lock()
	notify := make(chan struct{})
	s.waiters = append(s.waiters, notify)
	s.mu.Unlock()
	select {
	case <-notify:
	case <-time.After(wait):
	}
	return s.claimNow(worker, slots)
}

func (s *Scheduler) claimNow(worker *Worker, slots int) []*Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slots <= 0 || len(s.pending) == 0 {
		return nil
	}
	// 按打分排序候选任务，选最匹配的前 slots 个
	candidates := append([]*Job(nil), s.pending...)
	sort.SliceStable(candidates, func(i, j int) bool {
		return scoreJobForWorker(candidates[i], worker) > scoreJobForWorker(candidates[j], worker)
	})
	var taken []*Job
	for _, j := range candidates {
		if len(taken) >= slots {
			break
		}
		if !workerCanRun(worker, j) {
			continue
		}
		j.mu.Lock()
		j.Status = JobRunning
		j.WorkerID = worker.ID
		j.StartedAt = time.Now()
		j.mu.Unlock()
		taken = append(taken, j)
		// 从 pending 中移除
		for idx, pj := range s.pending {
			if pj == j {
				s.pending = append(s.pending[:idx], s.pending[idx+1:]...)
				break
			}
		}
	}
	return taken
}

// scoreJobForWorker 给 (worker, job) 打分（数值越大越匹配）
func scoreJobForWorker(j *Job, w *Worker) float64 {
	score := 1.0
	// 硬件加速匹配 + 50
	if len(j.Profiles) > 0 {
		req := strings.ToLower(j.Profiles[0].HWAccel)
		if req != "" && req != "none" && req != "auto" {
			for _, hw := range w.HWAccel {
				if strings.EqualFold(hw, req) {
					score += 50
					break
				}
			}
		}
	}
	// 空闲槽位多 +
	score += float64(w.FreeSlots) * 2
	// 负载低 +
	score += (1 - w.Load) * 10
	return score
}

// workerCanRun 检查 worker 是否能运行该 job
func workerCanRun(w *Worker, j *Job) bool {
	if w.FreeSlots <= 0 {
		return false
	}
	if j.Probe.VideoCodec != "" && len(w.CodecsDecode) > 0 {
		ok := false
		for _, c := range w.CodecsDecode {
			if strings.EqualFold(c, j.Probe.VideoCodec) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// Finish 由 worker 调用，标记任务完成/失败
func (s *Scheduler) Finish(req *FinishRequest) error {
	s.mu.Lock()
	j, ok := s.jobs[req.JobID]
	s.mu.Unlock()
	if !ok {
		return errors.New("job not found")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = req.Status
	j.Error = req.Error
	j.FinishedAt = time.Now()
	// 任务一旦完成或失败，确保 readyCh 一定会被关闭，避免播放端死等
	j.readyOnce.Do(func() { close(j.readyCh) })
	return nil
}

// Cancel 主动取消任务（同时通知对应 worker 并 kill 本地 FFmpeg 进程）
func (s *Scheduler) Cancel(jobID string) {
	s.mu.Lock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return
	}
	j.mu.Lock()
	if j.Status == JobPending || j.Status == JobRunning || j.Status == JobReady {
		j.Status = JobCancelled
	}
	j.readyOnce.Do(func() { close(j.readyCh) })
	wid := j.WorkerID
	j.mu.Unlock()
	if wid != "" {
		s.pendingCancels[wid] = append(s.pendingCancels[wid], jobID)
	}
	// 从 pending 中剔除
	for idx, pj := range s.pending {
		if pj.ID == jobID {
			s.pending = append(s.pending[:idx], s.pending[idx+1:]...)
			break
		}
	}
	cancelFn := s.onCancel
	s.mu.Unlock()
	// 在锁外调用回调，避免死锁；回调会 kill 本地 FFmpeg 进程
	if cancelFn != nil {
		cancelFn(jobID)
	}
}

// ListActiveJobs 列出所有活跃任务（pending/running/ready），用于空闲超时检测
func (s *Scheduler) ListActiveJobs() []*Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Job
	for _, j := range s.jobs {
		st := j.GetStatus()
		if st == JobRunning || st == JobReady || st == JobPending {
			out = append(out, j)
		}
	}
	return out
}

// PopCancellations 取出某个 worker 待处理的取消列表（心跳响应中下发）
func (s *Scheduler) PopCancellations(workerID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.pendingCancels[workerID]
	delete(s.pendingCancels, workerID)
	return ids
}

// ListJobs 列出全部任务（仅用于调试/管理）
func (s *Scheduler) ListJobs() []*Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	return out
}

// PurgeBefore 删除 finishedAt 早于某时刻的任务，避免 jobs map 无限增长
// 返回被清理的 job ID 列表，调用方需联动清理 Cache
func (s *Scheduler) PurgeBefore(t time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var purged []string
	for id, j := range s.jobs {
		if (j.Status == JobFinished || j.Status == JobFailed || j.Status == JobCancelled) &&
			!j.FinishedAt.IsZero() && j.FinishedAt.Before(t) {
			delete(s.jobs, id)
			purged = append(purged, id)
		}
	}
	return purged
}
