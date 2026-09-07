package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/tache"
	log "github.com/sirupsen/logrus"
)

type CleanupPhase string

const (
	CleanupDownloading    CleanupPhase = "downloading"
	CleanupTransferring   CleanupPhase = "transferring"
	CleanupBlocked        CleanupPhase = "blocked"
	CleanupWaitingSeeding CleanupPhase = "waiting_seeding"
	CleanupRunning        CleanupPhase = "cleaning"
)

type CleanupJob struct {
	ID                string       `json:"id"`
	DownloadTaskID    string       `json:"download_task_id,omitempty"`
	TempDir           string       `json:"temp_dir"`
	Toolname          string       `json:"toolname"`
	GID               string       `json:"gid,omitempty"`
	DeleteAfterTime   time.Time    `json:"delete_after_time,omitempty"`
	Phase             CleanupPhase `json:"phase"`
	TransferSetupDone bool         `json:"transfer_setup_done"`
	PendingTransfers  int          `json:"pending_transfers"`
	FailedTransfers   int          `json:"failed_transfers"`
	LastError         string       `json:"last_error,omitempty"`
}

type CleanupExecutor func(context.Context, CleanupJob) error

type CleanupManager struct {
	mu      sync.Mutex
	jobs    map[string]CleanupJob
	write   func([]byte) error
	execute CleanupExecutor
	wake    chan struct{}
	stop    chan struct{}
	stopped chan struct{}
	start   sync.Once
	close   sync.Once
}

var CleanupTaskManager *CleanupManager

func NewCleanupManager(read func() ([]byte, error), write func([]byte) error, execute CleanupExecutor) (*CleanupManager, error) {
	m := &CleanupManager{
		jobs:    make(map[string]CleanupJob),
		write:   write,
		execute: execute,
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	if m.execute == nil {
		m.execute = executeCleanupJob
	}
	if read == nil || write == nil {
		return nil, fmt.Errorf("cleanup persistence is not configured")
	}
	data, err := read()
	if err != nil {
		return nil, err
	}
	if len(data) > 0 && string(data) != "null" {
		var jobs []CleanupJob
		if err := json.Unmarshal(data, &jobs); err != nil {
			return nil, err
		}
		for _, job := range jobs {
			if job.ID == "" {
				continue
			}
			if job.Phase == CleanupRunning {
				job.Phase = CleanupWaitingSeeding
			}
			m.jobs[job.ID] = job
		}
	}
	return m, nil
}

func InitCleanupManager(read func() ([]byte, error), write func([]byte) error) error {
	m, err := NewCleanupManager(read, write, nil)
	if err != nil {
		return err
	}
	CleanupTaskManager = m
	return nil
}

func (m *CleanupManager) Start() {
	m.start.Do(func() {
		go m.loop()
		m.notify()
	})
}

func (m *CleanupManager) Close() {
	m.close.Do(func() {
		close(m.stop)
		<-m.stopped
	})
}

func (m *CleanupManager) loop() {
	defer close(m.stopped)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.RunDue(context.Background(), time.Now())
		case <-m.wake:
			m.RunDue(context.Background(), time.Now())
		case <-m.stop:
			return
		}
	}
}

func (m *CleanupManager) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *CleanupManager) persistLocked() error {
	jobs := make([]CleanupJob, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	data, err := json.Marshal(jobs)
	if err != nil {
		return err
	}
	return m.write(data)
}

func (m *CleanupManager) Register(job CleanupJob) error {
	if job.ID == "" || job.TempDir == "" {
		return fmt.Errorf("cleanup job id and temp dir are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[job.ID]; ok {
		return nil
	}
	job.Phase = CleanupDownloading
	m.jobs[job.ID] = job
	if err := m.persistLocked(); err != nil {
		delete(m.jobs, job.ID)
		return err
	}
	m.notify()
	return nil
}

func (m *CleanupManager) SetDownloadTaskID(id, taskID string) error {
	return m.update(id, func(job *CleanupJob) {
		job.DownloadTaskID = taskID
	})
}

func (m *CleanupManager) SetGID(id, gid string) error {
	return m.update(id, func(job *CleanupJob) {
		job.GID = gid
	})
}

func (m *CleanupManager) SetTempDir(id, tempDir string) error {
	if tempDir == "" {
		return nil
	}
	return m.update(id, func(job *CleanupJob) {
		job.TempDir = tempDir
	})
}

func (m *CleanupManager) BeginTransfer(id, gid string, deleteAfterTime time.Time) error {
	return m.update(id, func(job *CleanupJob) {
		job.GID = gid
		job.DeleteAfterTime = deleteAfterTime
		job.Phase = CleanupTransferring
		job.TransferSetupDone = false
		job.LastError = ""
	})
}

func (m *CleanupManager) AddTransfer(id string) error {
	return m.update(id, func(job *CleanupJob) {
		job.PendingTransfers++
		job.Phase = CleanupTransferring
		job.LastError = ""
	})
}

func (m *CleanupManager) FinishTransferSetup(id string) error {
	return m.update(id, func(job *CleanupJob) {
		job.TransferSetupDone = true
		evaluateCleanupJob(job)
	})
}

func (m *CleanupManager) TransferSucceeded(id string) error {
	return m.update(id, func(job *CleanupJob) {
		if job.PendingTransfers > 0 {
			job.PendingTransfers--
		}
		evaluateCleanupJob(job)
	})
}

func (m *CleanupManager) TransferFailed(id string, taskErr error) error {
	return m.update(id, func(job *CleanupJob) {
		if job.PendingTransfers > 0 {
			job.PendingTransfers--
		}
		job.FailedTransfers++
		job.Phase = CleanupBlocked
		if taskErr != nil {
			job.LastError = taskErr.Error()
		}
	})
}

func (m *CleanupManager) RetryTransfer(id string) error {
	return m.update(id, func(job *CleanupJob) {
		if job.FailedTransfers > 0 {
			job.FailedTransfers--
		}
		job.PendingTransfers++
		job.Phase = CleanupTransferring
		job.LastError = ""
	})
}

func (m *CleanupManager) DownloadFailed(id string, taskErr error) error {
	return m.update(id, func(job *CleanupJob) {
		job.Phase = CleanupBlocked
		if taskErr != nil {
			job.LastError = taskErr.Error()
		}
	})
}

func (m *CleanupManager) RetryDownload(id string) error {
	return m.update(id, func(job *CleanupJob) {
		job.Phase = CleanupDownloading
		job.LastError = ""
	})
}

func (m *CleanupManager) ReconcileDownloads(tasks []*DownloadTask) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for _, downloadTask := range tasks {
		if downloadTask.CleanupID == "" {
			continue
		}
		job, ok := m.jobs[downloadTask.CleanupID]
		if !ok {
			continue
		}
		previous := job
		job.DownloadTaskID = downloadTask.GetID()
		job.TempDir = downloadTask.TempDir
		job.GID = downloadTask.GID
		if !downloadTask.DeleteAfterTime.IsZero() {
			job.DeleteAfterTime = downloadTask.DeleteAfterTime
		}
		if job != previous {
			m.jobs[job.ID] = job
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return m.persistLocked()
}

func (m *CleanupManager) ReconcileTransfers(tasks []*TransferTask) error {
	type transferState struct {
		found   bool
		pending int
		failed  int
	}
	states := make(map[string]transferState)
	for _, transferTask := range tasks {
		if transferTask.CleanupID == "" {
			continue
		}
		state := states[transferTask.CleanupID]
		state.found = true
		switch transferTask.GetState() {
		case tache.StateSucceeded:
		case tache.StateFailed, tache.StateCanceled:
			state.failed++
		default:
			state.pending++
		}
		states[transferTask.CleanupID] = state
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for id, state := range states {
		if !state.found {
			continue
		}
		job, ok := m.jobs[id]
		if !ok {
			continue
		}
		previous := job
		job.PendingTransfers = state.pending
		job.FailedTransfers = state.failed
		evaluateCleanupJob(&job)
		if job != previous {
			m.jobs[id] = job
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return m.persistLocked()
}

func evaluateCleanupJob(job *CleanupJob) {
	if !job.TransferSetupDone || job.PendingTransfers > 0 {
		return
	}
	if job.FailedTransfers > 0 {
		job.Phase = CleanupBlocked
		return
	}
	job.Phase = CleanupWaitingSeeding
}

func (m *CleanupManager) update(id string, update func(*CleanupJob)) error {
	if id == "" {
		return nil
	}
	m.mu.Lock()
	job, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("cleanup job %s not found", id)
	}
	previous := job
	update(&job)
	if job == previous {
		m.mu.Unlock()
		return nil
	}
	m.jobs[id] = job
	err := m.persistLocked()
	if err != nil {
		m.jobs[id] = previous
	}
	m.mu.Unlock()
	if err == nil {
		m.notify()
	}
	return err
}

func (m *CleanupManager) RunDue(ctx context.Context, now time.Time) {
	for {
		m.mu.Lock()
		var due CleanupJob
		found := false
		for id, job := range m.jobs {
			if job.Phase != CleanupWaitingSeeding || job.DeleteAfterTime.IsZero() || job.DeleteAfterTime.After(now) {
				continue
			}
			job.Phase = CleanupRunning
			m.jobs[id] = job
			due = job
			found = true
			break
		}
		if found {
			if err := m.persistLocked(); err != nil {
				log.Errorf("failed to persist cleanup job before execution: %v", err)
				due.Phase = CleanupWaitingSeeding
				m.jobs[due.ID] = due
				m.mu.Unlock()
				return
			}
		}
		m.mu.Unlock()
		if !found {
			return
		}

		err := m.execute(ctx, due)
		m.mu.Lock()
		if err != nil {
			due.Phase = CleanupBlocked
			due.LastError = err.Error()
			m.jobs[due.ID] = due
		} else {
			delete(m.jobs, due.ID)
		}
		if persistErr := m.persistLocked(); persistErr != nil {
			log.Errorf("failed to persist cleanup result: %v", persistErr)
		}
		m.mu.Unlock()
	}
}

func (m *CleanupManager) Protects(path string) bool {
	path = filepath.Clean(path)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		rel, err := filepath.Rel(path, filepath.Clean(job.TempDir))
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (m *CleanupManager) Get(id string) (CleanupJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	return job, ok
}

func executeCleanupJob(ctx context.Context, job CleanupJob) error {
	if job.GID == "" {
		return fmt.Errorf("cleanup job %s has no download gid", job.ID)
	}
	tempDir := filepath.Clean(job.TempDir)
	root := filepath.Clean(conf.Conf.TempDir)
	rel, err := filepath.Rel(root, tempDir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to remove temp dir outside configured root: %s", tempDir)
	}
	downloadTool, err := Tools.Get(job.Toolname)
	if err != nil {
		return err
	}
	t := &DownloadTask{
		TaskExtension: task.TaskExtension{},
		TempDir:       tempDir,
		Toolname:      job.Toolname,
		GID:           job.GID,
	}
	t.SetCtx(ctx)
	if err := downloadTool.Remove(t); err != nil {
		return err
	}
	return os.RemoveAll(tempDir)
}
