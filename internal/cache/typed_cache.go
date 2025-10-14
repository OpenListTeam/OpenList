package cache

import (
	"sync"
	"time"
)

type TypedCache struct {
	entries map[string]map[string]*CacheEntry
	mu      sync.RWMutex
	ttl     time.Duration
}

func NewTypedCache(ttl time.Duration) *TypedCache {
	return &TypedCache{
		entries: make(map[string]map[string]*CacheEntry),
		ttl:     ttl,
	}
}

func (c *TypedCache) SetType(key, typeKey string, value interface{}) {
	c.SetTypeWithTTL(key, typeKey, value, c.ttl)
}

func (c *TypedCache) SetTypeWithTTL(key, typeKey string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cache, exists := c.entries[key]
	if !exists {
		cache = make(map[string]*CacheEntry)
	}

	cache[typeKey] = &CacheEntry{
		data:    value,
		expires: time.Now().Add(ttl),
		dirty:   false,
	}
}

func (c *TypedCache) GetType(key, typeKey string) (interface{}, bool) {
	now := time.Now()
	c.mu.RLock()
	cache, exists := c.entries[key]
	if !exists {
		c.mu.RUnlock()
		return nil, false
	}
	entry, exists := cache[typeKey]
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
	if c.entries[key] != nil && cache[typeKey] == entry {
		delete(cache, key)
		if len(cache) == 0 {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return nil, false
	}
	c.mu.Unlock()
	return nil, false
}

func (c *TypedCache) DeleteKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *TypedCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]map[string]*CacheEntry)
}
