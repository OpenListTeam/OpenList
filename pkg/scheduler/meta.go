package scheduler

import (
	"context"
	"sync"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
)

type JobRunner func(ctx context.Context, params ...any) error

type SchedulerFactory func(ctx context.Context) (gocron.Scheduler, error)

type taskFactory func() gocron.Task

type jobCancelMap = *SafeMap[uuid.UUID, context.CancelFunc]

func NewJobCancelMap() jobCancelMap {
	return NewSafeMap[uuid.UUID, context.CancelFunc]()
}

// 泛型的读写锁map
type SafeMap[K comparable, V any] struct {
	lock sync.RWMutex
	data map[K]V
}

func NewSafeMap[K comparable, V any]() *SafeMap[K, V] {
	return &SafeMap[K, V]{
		data: make(map[K]V),
	}
}

func (sm *SafeMap[K, V]) Get(key K) (V, bool) {
	sm.lock.RLock()
	defer sm.lock.RUnlock()
	value, exists := sm.data[key]
	return value, exists
}

func (sm *SafeMap[K, V]) Set(key K, value V) {
	sm.lock.Lock()
	defer sm.lock.Unlock()
	sm.data[key] = value
}

func (sm *SafeMap[K, V]) Delete(key K) {
	sm.lock.Lock()
	defer sm.lock.Unlock()
	delete(sm.data, key)
}

func (sm *SafeMap[K, V]) GetAll() map[K]V {
	sm.lock.RLock()
	defer sm.lock.RUnlock()
	result := make(map[K]V)
	for k, v := range sm.data {
		result[k] = v
	}
	return result
}
