package scheduler

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
)

type JobRunner func(ctx context.Context, params ...any) error

type jobCancelMap = *safeMap[uuid.UUID, context.CancelFunc]

type JobLabels = map[string]string

func newJobCancelMap() jobCancelMap {
	return newSafeMap[uuid.UUID, context.CancelFunc]()
}

// 泛型的读写锁map
type safeMap[K comparable, V any] struct {
	lock sync.RWMutex
	data map[K]V
}

func newSafeMap[K comparable, V any]() *safeMap[K, V] {
	return &safeMap[K, V]{
		data: make(map[K]V),
	}
}

func (sm *safeMap[K, V]) Get(key K) (V, bool) {
	sm.lock.RLock()
	defer sm.lock.RUnlock()
	value, exists := sm.data[key]
	return value, exists
}

func (sm *safeMap[K, V]) Set(key K, value V) {
	sm.lock.Lock()
	defer sm.lock.Unlock()
	sm.data[key] = value
}

func (sm *safeMap[K, V]) Delete(key K) {
	sm.lock.Lock()
	defer sm.lock.Unlock()
	delete(sm.data, key)
}

func (sm *safeMap[K, V]) GetAll() map[K]V {
	sm.lock.RLock()
	defer sm.lock.RUnlock()
	result := make(map[K]V)
	for k, v := range sm.data {
		result[k] = v
	}
	return result
}

func (sm *safeMap[K, V]) Clear() {
	sm.lock.Lock()
	defer sm.lock.Unlock()
	// 移除所有元素，但保持底层map不变
	for k := range sm.data {
		delete(sm.data, k)
	}
}

func (sm *safeMap[K, V]) ForEach(fn func(K, V)) {
	sm.lock.RLock()
	defer sm.lock.RUnlock()
	for k, v := range sm.data {
		fn(k, v)
	}
}

type OpJob struct {
	job      gocron.Job
	labels   JobLabels
	disabled bool
}

func (o *OpJob) ID() uuid.UUID {
	return o.job.ID()
}

func (o *OpJob) Name() string {
	return o.job.Name()
}

func (o *OpJob) Labels() JobLabels {
	return o.labels
}

func (o *OpJob) Label(key string) (string, bool) {
	value, exists := o.labels[key]
	return value, exists
}

func (o *OpJob) Disabled() bool {
	return o.disabled
}
func (o *OpJob) LastRun() (time.Time, error) {
	return o.job.LastRun()
}

func (o *OpJob) NextRun() (time.Time, error) {
	return o.job.NextRun()
}

func (o *OpJob) NextRuns(n int) ([]time.Time, error) {
	return o.job.NextRuns(n)
}

func newOpJob(job gocron.Job, disabled bool) *OpJob {
	labels := make(JobLabels)
	for _, tag := range job.Tags() {
		parts := strings.SplitN(tag, labelSep, 1)
		if len(parts) == 2 {
			labels[parts[0]] = parts[1]
		}
	}
	return &OpJob{
		job:      job,
		labels:   labels,
		disabled: disabled,
	}
}
