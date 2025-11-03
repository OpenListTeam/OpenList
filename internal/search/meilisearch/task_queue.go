package meilisearch

import (
	"context"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	mapset "github.com/deckarep/golang-set/v2"
	log "github.com/sirupsen/logrus"
)

// QueuedTask represents a task in the queue
type QueuedTask struct {
	Parent    string
	Objs      []model.Obj // current file system state
	Depth     int         // path depth for sorting
	EnqueueAt time.Time   // enqueue time
}

// TaskQueueManager manages the task queue for async index operations
type TaskQueueManager struct {
	queue        map[string]*QueuedTask // parent -> task
	pendingTasks map[string][]int64     // parent -> all submitted taskUIDs
	mu           sync.RWMutex
	ticker       *time.Ticker
	stopCh       chan struct{}
	m            *Meilisearch
	consuming    atomic.Bool // flag to prevent concurrent consumption
}

// NewTaskQueueManager creates a new task queue manager
func NewTaskQueueManager(m *Meilisearch) *TaskQueueManager {
	return &TaskQueueManager{
		queue:        make(map[string]*QueuedTask),
		pendingTasks: make(map[string][]int64),
		stopCh:       make(chan struct{}),
		m:            m,
	}
}

// calculateDepth calculates the depth of a path
func calculateDepth(path string) int {
	if path == "/" {
		return 0
	}
	return strings.Count(strings.Trim(path, "/"), "/") + 1
}

// Enqueue enqueues a task with current file system state
func (tqm *TaskQueueManager) Enqueue(parent string, objs []model.Obj) {
	tqm.mu.Lock()
	defer tqm.mu.Unlock()

	// deduplicate: overwrite existing task with the same parent
	tqm.queue[parent] = &QueuedTask{
		Parent:    parent,
		Objs:      objs,
		Depth:     calculateDepth(parent),
		EnqueueAt: time.Now(),
	}
	log.Debugf("enqueued update task for parent: %s, depth: %d, objs: %d", parent, calculateDepth(parent), len(objs))
}

// Start starts the task queue consumer
func (tqm *TaskQueueManager) Start() {
	tqm.ticker = time.NewTicker(30 * time.Second)
	go func() {
		for {
			select {
			case <-tqm.ticker.C:
				tqm.consume()
			case <-tqm.stopCh:
				log.Info("task queue manager stopped")
				return
			}
		}
	}()
	log.Info("task queue manager started, will consume every 30 seconds")
}

// Stop stops the task queue consumer
func (tqm *TaskQueueManager) Stop() {
	if tqm.ticker != nil {
		tqm.ticker.Stop()
	}
	close(tqm.stopCh)
}

// consume processes all tasks in the queue
func (tqm *TaskQueueManager) consume() {
	// Prevent concurrent consumption
	if !tqm.consuming.CompareAndSwap(false, true) {
		log.Warn("previous consume still running, skip this round")
		return
	}
	defer tqm.consuming.Store(false)

	tqm.mu.Lock()

	// extract all tasks
	tasks := make([]*QueuedTask, 0, len(tqm.queue))
	for _, task := range tqm.queue {
		tasks = append(tasks, task)
	}

	// clear queue
	tqm.queue = make(map[string]*QueuedTask)

	tqm.mu.Unlock()

	if len(tasks) == 0 {
		return
	}

	log.Infof("consuming task queue: %d tasks", len(tasks))

	// sort tasks: shallow paths first, then by enqueue time
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Depth != tasks[j].Depth {
			return tasks[i].Depth < tasks[j].Depth
		}
		return tasks[i].EnqueueAt.Before(tasks[j].EnqueueAt)
	})

	ctx := context.Background()

	// execute tasks in order
	for _, task := range tasks {
		// Check if there are pending tasks for this parent
		tqm.mu.RLock()
		pendingTaskUIDs, hasPending := tqm.pendingTasks[task.Parent]
		tqm.mu.RUnlock()

		if hasPending && len(pendingTaskUIDs) > 0 {
			// Check all pending task statuses
			allCompleted := true
			for _, taskUID := range pendingTaskUIDs {
				taskStatus, err := tqm.m.getTaskStatus(ctx, taskUID)
				if err != nil {
					log.Errorf("failed to get task status for parent %s (taskUID: %d): %v", task.Parent, taskUID, err)
					// If we can't get status, assume it's done and continue checking
					continue
				}

				// Check if task is still running
				if taskStatus == "enqueued" || taskStatus == "processing" {
					log.Warnf("skipping task for parent %s: previous task %d still %s", task.Parent, taskUID, taskStatus)
					allCompleted = false
					break // No need to check remaining tasks
				}
			}

			if !allCompleted {
				// Re-enqueue the task if not already in queue (avoid overwriting newer snapshots)
				tqm.mu.Lock()
				if _, exists := tqm.queue[task.Parent]; !exists {
					tqm.queue[task.Parent] = task
					log.Debugf("re-enqueued skipped task for parent %s due to pending tasks", task.Parent)
				} else {
					log.Debugf("skipped task for parent %s not re-enqueued (newer task already in queue)", task.Parent)
				}
				tqm.mu.Unlock()
				continue // Skip this task, some previous tasks are still running
			}

			// All tasks are in terminal state, remove from pending
			log.Debugf("all previous tasks for parent %s are completed, proceeding with new task", task.Parent)
			tqm.mu.Lock()
			delete(tqm.pendingTasks, task.Parent)
			tqm.mu.Unlock()
		}

		// Execute the task
		tqm.executeTask(ctx, task)
	}

	log.Infof("task queue consumption completed")
}

// executeTask executes a single task
func (tqm *TaskQueueManager) executeTask(ctx context.Context, task *QueuedTask) {
	parent := task.Parent
	currentObjs := task.Objs

	// Query index to get old state
	nodes, err := tqm.m.Get(ctx, parent)
	if err != nil {
		log.Errorf("failed to get indexed nodes for parent %s: %v", parent, err)
		return
	}

	// Calculate diff based on current index state
	now := mapset.NewSet[string]()
	for i := range currentObjs {
		now.Add(currentObjs[i].GetName())
	}
	old := mapset.NewSet[string]()
	for i := range nodes {
		old.Add(nodes[i].Name)
	}

	toDelete := old.Difference(now)
	toAdd := now.Difference(old)

	// Collect paths to delete
	var pathsToDelete []string
	for i := range nodes {
		if toDelete.Contains(nodes[i].Name) && !op.HasStorage(path.Join(parent, nodes[i].Name)) {
			pathsToDelete = append(pathsToDelete, path.Join(parent, nodes[i].Name))
		}
	}

	var allTaskUIDs []int64

	// Execute delete first
	if len(pathsToDelete) > 0 {
		log.Debugf("executing delete for parent %s: %d paths", parent, len(pathsToDelete))
		taskUIDs, err := tqm.m.batchDeleteWithTaskUID(ctx, pathsToDelete)
		if err != nil {
			log.Errorf("failed to batch delete for parent %s: %v", parent, err)
			// Continue to add even if delete fails
		} else {
			allTaskUIDs = append(allTaskUIDs, taskUIDs...)
		}
	}

	// Collect objects to add
	var nodesToAdd []model.SearchNode
	for i := range currentObjs {
		if toAdd.Contains(currentObjs[i].GetName()) {
			log.Debugf("will add index: %s", path.Join(parent, currentObjs[i].GetName()))
			nodesToAdd = append(nodesToAdd, model.SearchNode{
				Parent: parent,
				Name:   currentObjs[i].GetName(),
				IsDir:  currentObjs[i].IsDir(),
				Size:   currentObjs[i].GetSize(),
			})
		}
	}

	// Execute add
	if len(nodesToAdd) > 0 {
		log.Debugf("executing add for parent %s: %d nodes", parent, len(nodesToAdd))
		taskUIDs, err := tqm.m.batchIndexWithTaskUID(ctx, nodesToAdd)
		if err != nil {
			log.Errorf("failed to batch index for parent %s: %v", parent, err)
		} else {
			allTaskUIDs = append(allTaskUIDs, taskUIDs...)
		}
	}

	// Record all task UIDs for this parent
	if len(allTaskUIDs) > 0 {
		tqm.mu.Lock()
		tqm.pendingTasks[parent] = allTaskUIDs
		tqm.mu.Unlock()
		log.Debugf("recorded %d taskUIDs for parent %s", len(allTaskUIDs), parent)
	}
}
