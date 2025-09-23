package cache

import (
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type CacheEntry struct {
	data    interface{}
	expires time.Time
	dirty   bool
}

type UnifiedCache struct {
	entries map[string]*CacheEntry
	mu      sync.RWMutex
	ttl     time.Duration
}

func NewUnifiedCache(ttl time.Duration) *UnifiedCache {
	return &UnifiedCache{
		entries: make(map[string]*CacheEntry),
		ttl:     ttl,
	}
}

func (c *UnifiedCache) Set(key string, value interface{}) {
	c.SetWithTTL(key, value, c.ttl)
}

func (c *UnifiedCache) SetWithTTL(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = &CacheEntry{
		data:    value,
		expires: time.Now().Add(ttl),
		dirty:   false,
	}
}

func (c *UnifiedCache) Get(key string) (interface{}, bool) {
	now := time.Now()
	c.mu.RLock()
	entry, exists := c.entries[key]
	if !exists {
		c.mu.RUnlock()
		return nil, false
	}

	expired := now.After(entry.expires)
	c.mu.RUnlock()

	if expired {
		c.mu.Lock()
		// Re-check in case another goroutine already deleted or updated it
		entry, exists := c.entries[key]
		if exists && now.After(entry.expires) {
			delete(c.entries, key)
			c.mu.Unlock()
			return nil, false
		}
		if exists {
			val := entry.data
			c.mu.Unlock()
			return val, true
		}
		c.mu.Unlock()
		return nil, false
	}

	return entry.data, true
}

func (c *UnifiedCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, key)
}

func (c *UnifiedCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*CacheEntry)
}

type DirectoryCache struct {
	objects map[string]model.Obj
	sorted  []model.Obj
	dirty   bool
	mu      sync.RWMutex
}

func NewDirectoryCache() *DirectoryCache {
	return &DirectoryCache{
		objects: make(map[string]model.Obj),
		sorted:  make([]model.Obj, 0),
		dirty:   false,
	}
}

func (dc *DirectoryCache) AddObject(obj model.Obj) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.objects[obj.GetName()] = obj
	dc.dirty = true
}

func (dc *DirectoryCache) RemoveObject(name string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	delete(dc.objects, name)
	dc.dirty = true
}

func (dc *DirectoryCache) GetObject(name string) (model.Obj, bool) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	obj, exists := dc.objects[name]
	return obj, exists
}

func (dc *DirectoryCache) GetSortedObjects() []model.Obj {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if dc.dirty {
		dc.rebuildSortedList()
	}
	result := make([]model.Obj, len(dc.sorted))
	copy(result, dc.sorted)
	return result
}

func (dc *DirectoryCache) rebuildSortedList() {
	dc.sorted = dc.sorted[:0]
	for _, obj := range dc.objects {
		dc.sorted = append(dc.sorted, obj)
	}
	dc.dirty = false
}

func (dc *DirectoryCache) Size() int {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return len(dc.objects)
}
