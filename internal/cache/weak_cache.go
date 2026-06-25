package cache

import (
	"runtime"
	"sync"
	"weak"
)

// WeakCacheMap is a map that holds weak references to values.
// Use for shared expensive objects and automatic cleanup when no longer used.
// This object can be GC and no goroutine is used for cleanup.
type WeakCacheMap[K comparable, V any] struct {
	mu sync.Mutex
	m  map[K]*weakCacheEntry[V]
}

type weakCacheEntry[V any] struct {
	weakPtr weak.Pointer[V]
	cleanup runtime.Cleanup
}

func NewWeakCacheMap[K comparable, V any]() *WeakCacheMap[K, V] {
	return &WeakCacheMap[K, V]{
		m: make(map[K]*weakCacheEntry[V]),
	}
}

func (c *WeakCacheMap[K, V]) Load(key K) (value *V, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, exists := c.m[key]
	if !exists {
		return nil, false
	}
	value = entry.weakPtr.Value()
	return value, value != nil
}

func (c *WeakCacheMap[K, V]) Store(key K, value *V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, exists := c.m[key]
	if exists {
		entry.cleanup.Stop()
	} else {
		entry = &weakCacheEntry[V]{}
		c.m[key] = entry
	}
	entry.weakPtr = weak.Make(value)
	entry.cleanup = runtime.AddCleanup(value, func(struct{}) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.m[key] == entry {
			delete(c.m, key)
		}
	}, struct{}{})
}

func (c *WeakCacheMap[K, V]) Delete(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, exists := c.m[key]
	if !exists {
		return false
	}
	entry.cleanup.Stop()
	delete(c.m, key)
	return true
}

func (c *WeakCacheMap[K, V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.m {
		entry.cleanup.Stop()
	}
	clear(c.m)
}
