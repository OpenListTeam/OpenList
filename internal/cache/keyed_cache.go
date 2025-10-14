package cache

import (
	"sync"
	"time"
)

type CacheEntry struct {
	data    interface{}
	expires time.Time
	dirty   bool
}

type KeyedCache struct {
	entries map[string]*CacheEntry
	mu      sync.RWMutex
	ttl     time.Duration
}

func NewKeyedCache(ttl time.Duration) *KeyedCache {
	return &KeyedCache{
		entries: make(map[string]*CacheEntry),
		ttl:     ttl,
	}
}

func (c *KeyedCache) Set(key string, value interface{}) {
	c.SetWithTTL(key, value, c.ttl)
}

func (c *KeyedCache) SetWithTTL(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = &CacheEntry{
		data:    value,
		expires: time.Now().Add(ttl),
		dirty:   false,
	}
}

func (c *KeyedCache) Get(key string) (interface{}, bool) {
	now := time.Now()
	c.mu.RLock()
	entry, exists := c.entries[key]
	if !exists {
		c.mu.RUnlock()
		return nil, false
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
		return nil, false
	}
	c.mu.Unlock()
	return nil, false
}

func (c *KeyedCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, key)
}

func (c *KeyedCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*CacheEntry)
}
