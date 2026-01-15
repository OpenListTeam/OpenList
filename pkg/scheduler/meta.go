package scheduler

import (
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
)

// JobRunner defines the expected function signature for job runners.
//
// Implementations must be functions that accept a context.Context as the first
// parameter, followed by zero or more additional parameters, and return an error.
//
// A canonical example is:
//
//	func(ctx context.Context, args ...any) error
//
// While JobRunner is typed as any for flexibility, callers are expected to
// adhere to this function shape.
type JobRunner any

// JobLabels is the type for job labels
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
// The callback is executed while holding a read lock; it should be fast and must not call
// methods on the same safeMap that acquire the lock (e.g., Set, Delete, Clear) to avoid deadlocks.
func (sm *safeMap[K, V]) ForEach(fn func(K, V)) {
	sm.lock.RLock()
	defer sm.lock.RUnlock()
	for k, v := range sm.data {
		fn(k, v)
	}
}

// OpJob represents an operational job with its metadata.
type OpJob struct {
	id          uuid.UUID
	name        string
	lastRun     time.Time
	lastRunErr  error
	nextTenRuns []time.Time
	nextRunErr  error
	labels      JobLabels
	disabled    bool
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
	if len(o.nextTenRuns) == 0 {
		return time.Time{}, errors.New("no next run scheduled,maybe the scheduler is not started or has been stopped")
	}
	return o.nextTenRuns[0], nil
}

// GetNextTenRuns returns the next 10 run times of the job.
func (o *OpJob) GetNextTenRuns() ([]time.Time, error) {
	return o.nextTenRuns, o.nextRunErr
}

// newOpJob creates a new OpJob instance from a gocron.Job and its disabled status.
func newOpJob(job gocron.Job, disabled bool) *OpJob {
	labels := tags2JobLabels(job.Tags())
	lastRun, lastRunErr := job.LastRun()
	nextRun10, nextRunErr := job.NextRuns(10)
	labelsCopy := make(JobLabels)
	maps.Copy(labelsCopy, labels)
	return &OpJob{
		id:          job.ID(),
		name:        job.Name(),
		lastRun:     lastRun,
		lastRunErr:  lastRunErr,
		nextTenRuns: nextRun10,
		nextRunErr:  nextRunErr,
		labels:      labelsCopy,
		disabled:    disabled,
	}
}

type AtTime struct {
	hours, minutes, seconds uint
}

// NewAtTime constructs a new AtTime instance.
func NewAtTime(hours, minutes, seconds uint) AtTime {
	return AtTime{
		hours:   hours,
		minutes: minutes,
		seconds: seconds,
	}
}

// AtTimes defines a list of AtTime
type AtTimes []AtTime
