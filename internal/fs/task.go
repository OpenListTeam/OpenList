package fs

import (
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// BatchTracker manages batch operations for cache refresh optimization
// It aggregates multiple file operations by target directory and only refreshes
// the cache once when all operations in a directory are completed
type BatchTracker struct {
	mu           sync.Mutex
	dirTasks     map[string]*dirTaskInfo // dstStoragePath+dstDirPath -> dirTaskInfo
	pendingTasks map[string]string       // taskID -> dstStoragePath+dstDirPath
	lastCleanup  time.Time               // last cleanup time
	name         string                  // tracker name for debugging
}

type dirTaskInfo struct {
	dstStorage     driver.Driver
	dstDirPath     string
	pendingTasks   map[string]bool // taskID -> true
	lastActivity   time.Time       // last activity time (used for detecting abnormal situations)
}

// NewBatchTracker creates a new batch tracker instance
func NewBatchTracker(name string) *BatchTracker {
	return &BatchTracker{
		dirTasks:     make(map[string]*dirTaskInfo),
		pendingTasks: make(map[string]string),
		lastCleanup:  time.Now(),
		name:         name,
	}
}

// getDirKey generates unique key for target directory
func (bt *BatchTracker) getDirKey(dstStorage driver.Driver, dstDirPath string) string {
	return dstStorage.GetStorage().MountPath + ":" + dstDirPath
}

// RegisterTask registers a task to target directory for batch tracking
func (bt *BatchTracker) RegisterTask(taskID string, dstStorage driver.Driver, dstDirPath string) {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	
	// Periodically clean up expired entries
	bt.cleanupIfNeeded()
	
	dirKey := bt.getDirKey(dstStorage, dstDirPath)
	
	// Record task to directory mapping
	bt.pendingTasks[taskID] = dirKey
	
	// Initialize or update directory task information
	if info, exists := bt.dirTasks[dirKey]; exists {
		info.pendingTasks[taskID] = true
		info.lastActivity = time.Now()
	} else {
		bt.dirTasks[dirKey] = &dirTaskInfo{
			dstStorage:   dstStorage,
			dstDirPath:   dstDirPath,
			pendingTasks: map[string]bool{taskID: true},
			lastActivity: time.Now(),
		}
	}
}

// MarkTaskCompleted marks a task as completed and returns whether cache refresh is needed
// Returns (shouldRefresh, dstStorage, dstDirPath)
func (bt *BatchTracker) MarkTaskCompleted(taskID string) (bool, driver.Driver, string) {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	
	dirKey, exists := bt.pendingTasks[taskID]
	if !exists {
		return false, nil, ""
	}
	
	// Remove from pending tasks
	delete(bt.pendingTasks, taskID)
	
	info, exists := bt.dirTasks[dirKey]
	if !exists {
		return false, nil, ""
	}
	
	// Remove from directory tasks
	delete(info.pendingTasks, taskID)
	
	// If no pending tasks left in this directory, trigger cache refresh
	if len(info.pendingTasks) == 0 {
		dstStorage := info.dstStorage
		dstDirPath := info.dstDirPath
		delete(bt.dirTasks, dirKey)  // Delete directly, no need to update lastActivity
		return true, dstStorage, dstDirPath
	}
	
	// Only update lastActivity when there are other tasks (indicating the directory still has active tasks)
	info.lastActivity = time.Now()
	return false, nil, ""
}

// MarkTaskCompletedWithRefresh marks a task as completed and automatically refreshes cache if needed
func (bt *BatchTracker) MarkTaskCompletedWithRefresh(taskID string) {
	shouldRefresh, dstStorage, dstDirPath := bt.MarkTaskCompleted(taskID)
	if shouldRefresh {
		op.ClearCache(dstStorage, dstDirPath)
	}
}

// cleanupIfNeeded checks if cleanup is needed and executes cleanup if necessary
func (bt *BatchTracker) cleanupIfNeeded() {
	now := time.Now()
	// Clean up every 10 minutes
	if now.Sub(bt.lastCleanup) > 10*time.Minute {
		bt.cleanupStaleEntries()
		bt.lastCleanup = now
	}
}

// cleanupStaleEntries cleans up timed-out tasks to prevent memory leaks
// Mainly used to clean up residual entries caused by abnormal situations (such as task crashes, process restarts, etc.)
func (bt *BatchTracker) cleanupStaleEntries() {
	now := time.Now()
	for dirKey, info := range bt.dirTasks {
		// If no activity for more than 1 hour, it may indicate an abnormal situation, clean up this entry
		// Under normal circumstances, MarkTaskCompleted will be called when the task is completed and the entire entry will be deleted
		if now.Sub(info.lastActivity) > time.Hour {
			// Clean up related pending tasks
			for taskID := range info.pendingTasks {
				delete(bt.pendingTasks, taskID)
			}
			delete(bt.dirTasks, dirKey)
		}
	}
}

// GetPendingTaskCount returns the number of pending tasks for debugging
func (bt *BatchTracker) GetPendingTaskCount() int {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	return len(bt.pendingTasks)
}

// GetDirTaskCount returns the number of directories being tracked for debugging
func (bt *BatchTracker) GetDirTaskCount() int {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	return len(bt.dirTasks)
}
