package cache

import (
	"sync"
	"time"
)

type CacheEntry[T any] struct {
	data    T
	expires time.Time
	dirty   bool
}

type KeyedCache[T any] struct {
	entries map[string]*CacheEntry[T]
	mu      sync.RWMutex
	ttl     time.Duration
}

func NewKeyedCache[T any](ttl time.Duration) *KeyedCache[T] {
	return &KeyedCache[T]{
		entries: make(map[string]*CacheEntry[T]),
		ttl:     ttl,
	}
}

func (c *KeyedCache[T]) Set(key string, value T) {
	c.SetWithTTL(key, value, c.ttl)
}

func (c *KeyedCache[T]) SetWithTTL(key string, value T, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = &CacheEntry[T]{
		data:    value,
		expires: time.Now().Add(ttl),
		dirty:   false,
	}
}

func (c *KeyedCache[T]) Get(key string) (T, bool) {
	now := time.Now()
	c.mu.RLock()
	entry, exists := c.entries[key]
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
	if c.entries[key] == entry {
		delete(c.entries, key)
		c.mu.Unlock()
		return *new(T), false
	}
	c.mu.Unlock()
	return *new(T), false
}

func (c *KeyedCache[T]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, key)
}

func (c *KeyedCache[T]) Take(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, exists := c.entries[key]; exists {
		delete(c.entries, key)
		return entry.data, true
	}
	return *new(T), false
}

func (c *KeyedCache[T]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*CacheEntry[T])
}
