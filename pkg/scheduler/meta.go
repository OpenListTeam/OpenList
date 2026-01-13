package scheduler

import (
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
)

// JobRunner defines the function signature for job runners
type JobRunner any

// JobLabels the type for job labels
type JobLabels = map[string]string

// safeMap is a thread-safe map implementation
type safeMap[K comparable, V any] struct {
	lock sync.RWMutex
	data map[K]V
}

func newSafeMap[K comparable, V any]() *safeMap[K, V] {
	return &safeMap[K, V]{
		data: make(map[K]V),
	}
}

// Get retrieves a value by key from the safeMap.
func (sm *safeMap[K, V]) Get(key K) (V, bool) {
	sm.lock.RLock()
	defer sm.lock.RUnlock()
	value, exists := sm.data[key]
	return value, exists
}

// Set sets a key-value pair in the safeMap.
func (sm *safeMap[K, V]) Set(key K, value V) {
	sm.lock.Lock()
	defer sm.lock.Unlock()
	sm.data[key] = value
}

// Delete removes a key-value pair from the safeMap by key.
func (sm *safeMap[K, V]) Delete(key K) {
	sm.lock.Lock()
	defer sm.lock.Unlock()
	delete(sm.data, key)
}

// GetAll retrieves all key-value pairs from the safeMap.
func (sm *safeMap[K, V]) GetAll() map[K]V {
	sm.lock.RLock()
	defer sm.lock.RUnlock()
	result := make(map[K]V)
	maps.Copy(result, sm.data)
	return result
}

// Clear removes all key-value pairs from the safeMap.
func (sm *safeMap[K, V]) Clear() {
	sm.lock.Lock()
	defer sm.lock.Unlock()
	// reinitialize the map to clear all entries
	sm.data = make(map[K]V)
}

// ForEach iterates over all key-value pairs in the safeMap and applies the provided function.
func (sm *safeMap[K, V]) ForEach(fn func(K, V)) {
	snapshot := sm.GetAll()
	for k, v := range snapshot {
		fn(k, v)
	}
}

// OpJob represents an operational job with its metadata.
type OpJob struct {
	id         uuid.UUID
	name       string
	lastRun    time.Time
	lastRunErr error
	nextRun10  []time.Time
	nextRunErr error
	labels     JobLabels
	disabled   bool
}

// ID returns the UUID of the job.
func (o *OpJob) ID() uuid.UUID {
	return o.id
}

// Name returns the name of the job.
func (o *OpJob) Name() string {
	return o.name
}

// Labels returns the labels of the job.
func (o *OpJob) Labels() JobLabels {
	return o.labels
}

// Label retrieves the value of a specific label by its key.
func (o *OpJob) Label(key string) (string, bool) {
	value, exists := o.labels[key]
	return value, exists
}

// Disabled indicates whether the job is disabled.
func (o *OpJob) Disabled() bool {
	return o.disabled
}

// LastRun returns the last run time of the job.
func (o *OpJob) LastRun() (time.Time, error) {
	return o.lastRun, o.lastRunErr
}

// NextRun returns the next run time of the job.
func (o *OpJob) NextRun() (time.Time, error) {
	if o.nextRunErr != nil {
		return time.Time{}, o.nextRunErr
	}
	return o.nextRun10[0], nil
}

// NextRuns returns the next n run times of the job.
func (o *OpJob) NextRuns10() ([]time.Time, error) {
	return o.nextRun10, o.nextRunErr
}

// newOpJob creates a new OpJob instance from a gocron.Job and its disabled status.
func newOpJob(job gocron.Job, disabled bool) *OpJob {
	labels := make(JobLabels)
	for _, tag := range job.Tags() {
		parts := strings.SplitN(tag, ":", 2)
		if len(parts) == 2 {
			labels[unescapeTagStr(parts[0])] = unescapeTagStr(parts[1])
		}
	}
	lastRun, lastRunErr := job.LastRun()
	nextRun10, nextRunErr := job.NextRuns(10)
	labelsCopy := make(JobLabels)
	maps.Copy(labelsCopy, labels)
	return &OpJob{
		id:         job.ID(),
		name:       job.Name(),
		lastRun:    lastRun,
		lastRunErr: lastRunErr,
		nextRun10:  nextRun10,
		nextRunErr: nextRunErr,
		labels:     labelsCopy,
		disabled:   disabled,
	}
}
