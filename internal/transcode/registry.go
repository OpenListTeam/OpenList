package transcode

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// WorkerRegistry 维护当前在线的 Worker 池
type WorkerRegistry struct {
	mu      sync.RWMutex
	workers map[string]*Worker

	heartbeatTimeout time.Duration
}

func NewWorkerRegistry() *WorkerRegistry {
	return &WorkerRegistry{
		workers:          make(map[string]*Worker),
		heartbeatTimeout: 30 * time.Second,
	}
}

// Register 注册一个新的 worker，返回分配的 ID
func (r *WorkerRegistry) Register(req *RegisterRequest, local bool) *Worker {
	w := &Worker{
		ID:            "wk_" + uuid.NewString(),
		Name:          req.Name,
		Version:       req.Version,
		Capacity:      req.Capacity,
		HWAccel:       req.HWAccel,
		CodecsDecode:  req.CodecsDecode,
		CodecsEncode:  req.CodecsEncode,
		MaxResolution: req.MaxResolution,
		Tags:          req.Tags,
		Endpoint:      req.Endpoint,
		FreeSlots:     req.Capacity,
		LastSeen:      time.Now(),
		RegisteredAt:  time.Now(),
		Local:         local,
	}
	if w.Capacity <= 0 {
		w.Capacity = 1
		w.FreeSlots = 1
	}
	r.mu.Lock()
	r.workers[w.ID] = w
	r.mu.Unlock()
	return w
}

// Heartbeat 更新 worker 心跳信息，未注册返回 false
func (r *WorkerRegistry) Heartbeat(req *HeartbeatRequest) (*Worker, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[req.WorkerID]
	if !ok {
		return nil, false
	}
	w.LastSeen = time.Now()
	w.Load = req.Load
	w.Running = req.Running
	w.FreeSlots = req.FreeSlots
	return w, true
}

// Get 获取 worker
func (r *WorkerRegistry) Get(id string) (*Worker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[id]
	return w, ok
}

// Unregister 主动移除 worker
func (r *WorkerRegistry) Unregister(id string) {
	r.mu.Lock()
	delete(r.workers, id)
	r.mu.Unlock()
}

// List 列出所有 worker（拷贝）
func (r *WorkerRegistry) List() []*Worker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Worker, 0, len(r.workers))
	for _, w := range r.workers {
		out = append(out, w)
	}
	return out
}

// Sweep 清理超时未心跳的 worker；返回被移除的 worker_id 列表
func (r *WorkerRegistry) Sweep() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var removed []string
	for id, w := range r.workers {
		if w.Local {
			continue // 本地 Worker 不通过心跳判活
		}
		if now.Sub(w.LastSeen) > r.heartbeatTimeout {
			removed = append(removed, id)
			delete(r.workers, id)
		}
	}
	return removed
}

// HasAvailable 是否存在有空闲槽位的 worker
func (r *WorkerRegistry) HasAvailable() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, w := range r.workers {
		if w.FreeSlots > 0 {
			return true
		}
	}
	return false
}
