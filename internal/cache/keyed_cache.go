package cache

import (
	"sync"
	"time"
)

type KeyedCache[T any] struct {
	entries map[string]*CacheEntry[T]
	mu      sync.RWMutex
	ttl     time.Duration
}

func NewKeyedCache[T any](ttl time.Duration) *KeyedCache[T] {
	c := &KeyedCache[T]{
		entries: make(map[string]*CacheEntry[T]),
		ttl:     ttl,
	}
	gcFuncs = append(gcFuncs, c.GC)
	return c
}

func (c *KeyedCache[T]) Set(key string, value T) {
	c.SetWithExpirable(key, value, ExpirationTime(time.Now().Add(c.ttl)))
}

func (c *KeyedCache[T]) SetWithTTL(key string, value T, ttl time.Duration) {
	c.SetWithExpirable(key, value, ExpirationTime(time.Now().Add(ttl)))
}

func (c *KeyedCache[T]) SetWithExpirable(key string, value T, exp Expirable) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = &CacheEntry[T]{
		data:      value,
		Expirable: exp,
	}
}

func (c *KeyedCache[T]) Get(key string) (T, bool) {
	c.mu.RLock()
	entry, exists := c.entries[key]
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

func (c *KeyedCache[T]) Pop(key string) (T, bool) {
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

func (c *KeyedCache[T]) GC() {
	c.mu.Lock()
	defer c.mu.Unlock()
	expiredKeys := make([]string, 0, len(c.entries))
	for key, entry := range c.entries {
		if entry.Expired() {
			expiredKeys = append(expiredKeys, key)
		}
	}
	for _, key := range expiredKeys {
		delete(c.entries, key)
	}
}
