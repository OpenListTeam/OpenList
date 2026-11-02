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

// 提交配额。转码是 CPU/磁盘密集型操作，必须限制单用户并发与全局排队长度，
// 否则任何能访问播放接口的用户都能无限堆积任务（每个任务还会触发 30s 的 ffprobe）。
const (
	// MaxPendingJobs 全局排队（未被 worker 领取）任务上限
	MaxPendingJobs = 200
	// MaxActiveJobsPerUser 单用户同时存在的未结束任务上限
	MaxActiveJobsPerUser = 3
)

var (
	// ErrQueueFull 全局队列已满
	ErrQueueFull = errors.New("transcode queue is full, try again later")
	// ErrUserQuota 单用户并发任务已达上限
	ErrUserQuota = errors.New("too many active transcode jobs for this user")
	// ErrJobNotFound 任务不存在或已被清理
	ErrJobNotFound = errors.New("transcode job not found")
)

// isActiveStatus 任务是否仍占用资源（未结束）
func isActiveStatus(st JobStatus) bool {
	return st == JobPending || st == JobRunning || st == JobReady
}

// FindReusable 查找同一用户对同一路径、仍在进行中的任务，用于去重复用。
func (s *Scheduler) FindReusable(ownerID string, path string) (*Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findReusableLocked(ownerID, path)
}

func (s *Scheduler) findReusableLocked(ownerID string, path string) (*Job, bool) {
	for _, j := range s.jobs {
		if j.OwnerID == ownerID && j.Path == path && isActiveStatus(j.GetStatus()) {
			return j, true
		}
	}
	return nil, false
}

// SubmitLimited 带去重与配额的提交：
//  1. 同一用户对同一路径已有进行中的任务时直接复用，不重复入队；
//  2. 全局排队过长或用户任务过多时拒绝。
//
// 返回 (job, reused, err)。
func (s *Scheduler) SubmitLimited(j *Job) (*Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if exist, ok := s.findReusableLocked(j.OwnerID, j.Path); ok {
		return exist, true, nil
	}
	if len(s.pending) >= MaxPendingJobs {
		return nil, false, ErrQueueFull
	}
	active := 0
	for _, x := range s.jobs {
		if x.OwnerID == j.OwnerID && isActiveStatus(x.GetStatus()) {
			active++
		}
	}
	if active >= MaxActiveJobsPerUser {
		return nil, false, ErrUserQuota
	}
	// 检查与入队在同一把锁内完成，避免并发请求同时越过配额
	return s.enqueueLocked(j), false, nil
}

// Capacity 只读预检：在启动 ffprobe 这类昂贵操作之前先看配额是否已满。
//
// 真正的配额判定仍由 SubmitLimited 在同一把锁内完成；这里的目的有两个：
//  1. 不让用户先等一次最长 30s 的 ffprobe 才收到 429；
//  2. 把「并发请求同时通过配额判定」这个窗口内的探测数量压下来。
func (s *Scheduler) Capacity(ownerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) >= MaxPendingJobs {
		return ErrQueueFull
	}
	active := 0
	for _, x := range s.jobs {
		if x.OwnerID == ownerID && isActiveStatus(x.GetStatus()) {
			active++
		}
	}
	if active >= MaxActiveJobsPerUser {
		return ErrUserQuota
	}
	return nil
}

// Submit 提交一个任务并返回（带 ID + token）。不做配额限制，仅供内部流程（如重新激活）使用。
func (s *Scheduler) Submit(j *Job) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enqueueLocked(j)
}

// enqueueLocked 补全任务字段并入队，调用方必须已持有 s.mu。
func (s *Scheduler) enqueueLocked(j *Job) *Job {
	if j.ID == "" {
		j.ID = "job_" + uuid.NewString()
	}
	if j.CallbackToken == "" {
		j.CallbackToken = "tk_" + uuid.NewString()
	}
	// 播放凭据与 Worker 回写凭据分离：播放端只能读，不能上传切片或结束任务
	if j.PlayToken == "" {
		j.PlayToken = "pt_" + uuid.NewString()
	}
	if j.readyCh == nil {
		j.readyCh = make(chan struct{})
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now()
	}
	j.Status = JobPending

	s.jobs[j.ID] = j
	s.pending = append(s.pending, j)
	// 唤醒等待的 worker
	for _, ch := range s.waiters {
		close(ch)
	}
	s.waiters = nil
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

// Reactivate 把一个已 Cancelled / Failed / Finished 的 job 重新激活回 Pending 状态，
// 让 worker 能再次领取并跑转码。用于"播放端重新请求时按需重启转码"的场景。
//
// 设计点：
//  1. 复用原 jobID 和两套 token（播放用 PlayToken / Worker 回写用 CallbackToken），
//     前端持有的 master.m3u8 中的 URL 不需要变更
//  2. 重置 readyCh / Status / WorkerID 等运行时字段，但保留 Path / SourceURL / Profiles / Probe
//  3. 重新加入 pending 队列并唤醒等待的 worker
//
// 返回被激活的 Job；job 不存在返回 ErrJobNotFound，单用户配额不足返回 ErrUserQuota，
// 全局排队已满返回 ErrQueueFull。
func (s *Scheduler) Reactivate(jobID string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[jobID]
	if !ok {
		return nil, ErrJobNotFound
	}
	// 已经活跃：无需重新入队
	if isActiveStatus(j.GetStatus()) {
		return j, nil
	}

	// 重新激活同样受全局排队上限约束：否则可以靠「先让任务 idle 取消、再批量激活」
	// 把 pending 队列顶到 MaxPendingJobs 之外。
	if len(s.pending) >= MaxPendingJobs {
		return nil, ErrQueueFull
	}

	// 重新激活同样占用配额。否则持有 PlayToken 的用户可以先让任务因 idle 被取消，
	// 再统一请求 playlist / seg-N.ts 把它们一起重新激活，绕过 SubmitLimited 的单用户上限。
	active := 0
	for _, x := range s.jobs {
		if x == j {
			continue
		}
		if x.OwnerID == j.OwnerID && isActiveStatus(x.GetStatus()) {
			active++
		}
	}
	if active >= MaxActiveJobsPerUser {
		return nil, ErrUserQuota
	}

	j.mu.Lock()
	// 期间可能已被其它请求激活
	if isActiveStatus(j.Status) {
		j.mu.Unlock()
		return j, nil
	}
	// 重置运行时状态
	j.Status = JobPending
	j.WorkerID = ""
	j.Error = ""
	j.FinishedAt = time.Time{}
	j.LastAccessAt = time.Now()
	// 重置 readyCh：原 channel 已 closed，需要全新一份给后续 WaitReady
	j.readyCh = make(chan struct{})
	j.readyOnce = sync.Once{}
	j.mu.Unlock()

	// 重新入 pending 队列（先确认不在队列中以免重复）
	inQueue := false
	for _, pj := range s.pending {
		if pj.ID == jobID {
			inQueue = true
			break
		}
	}
	if !inQueue {
		s.pending = append(s.pending, j)
	}
	// 唤醒等待的 worker
	for _, ch := range s.waiters {
		close(ch)
	}
	s.waiters = nil
	return j, nil
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
