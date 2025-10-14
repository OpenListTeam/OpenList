package cache

import (
	"sync"
	"time"
)

type TypedCache[T any] struct {
	entries map[string]map[string]*CacheEntry[T]
	mu      sync.RWMutex
	ttl     time.Duration
}

func NewTypedCache[T any](ttl time.Duration) *TypedCache[T] {
	return &TypedCache[T]{
		entries: make(map[string]map[string]*CacheEntry[T]),
		ttl:     ttl,
	}
}

func (c *TypedCache[T]) SetType(key, typeKey string, value T) {
	c.SetTypeWithTTL(key, typeKey, value, c.ttl)
}

func (c *TypedCache[T]) SetTypeWithTTL(key, typeKey string, value T, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cache, exists := c.entries[key]
	if !exists {
		cache = make(map[string]*CacheEntry[T])
	}

	cache[typeKey] = &CacheEntry[T]{
		data:    value,
		expires: time.Now().Add(ttl),
		dirty:   false,
	}
}

func (c *TypedCache[T]) GetType(key, typeKey string) (T, bool) {
	now := time.Now()
	c.mu.RLock()
	cache, exists := c.entries[key]
	if !exists {
		c.mu.RUnlock()
		return *new(T), false
	}
	entry, exists := cache[typeKey]
	if !exists {
		c.mu.RUnlock()
		return *new(T), false
	}

	expired := now.After(entry.expires)
	c.mu.RUnlock()

	if !expired {
		return entry.data, true
	}

	c.mu.Lock()
	// Re-check in case another goroutine already deleted or updated it
	if cache[typeKey] == entry {
		delete(cache, key)
		if len(cache) == 0 {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return *new(T), false
	}
	c.mu.Unlock()
	return *new(T), false
}

func (c *TypedCache[T]) DeleteKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *TypedCache[T]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]map[string]*CacheEntry[T])
}
