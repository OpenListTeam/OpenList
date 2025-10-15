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
	c.SetTypeWithExpirable(key, typeKey, value, ExpirationTime(time.Now().Add(c.ttl)))
}

func (c *TypedCache[T]) SetTypeWithTTL(key, typeKey string, value T, ttl time.Duration) {
	c.SetTypeWithExpirable(key, typeKey, value, ExpirationTime(time.Now().Add(ttl)))
}

func (c *TypedCache[T]) SetTypeWithExpirable(key, typeKey string, value T, exp Expirable) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cache, exists := c.entries[key]
	if !exists {
		cache = make(map[string]*CacheEntry[T])
		c.entries[key] = cache
	}

	cache[typeKey] = &CacheEntry[T]{
		data:      value,
		Expirable: exp,
		dirty:     false,
	}
}

func (c *TypedCache[T]) GetType(key, typeKey string) (T, bool) {
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

	expired := entry.Expired()
	c.mu.RUnlock()

	if !expired {
		return entry.data, true
	}

	c.mu.Lock()
	// Re-check in case another goroutine already deleted or updated it
	if cache[typeKey] == entry {
		delete(cache, typeKey)
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
