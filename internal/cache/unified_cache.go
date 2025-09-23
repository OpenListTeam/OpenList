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
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.entries[key]
	if !exists {
		return nil, false
	}

	if time.Now().After(entry.expires) {
		delete(c.entries, key)
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
}

func NewDirectoryCache() *DirectoryCache {
	return &DirectoryCache{
		objects: make(map[string]model.Obj),
		sorted:  make([]model.Obj, 0),
		dirty:   false,
	}
}

func (dc *DirectoryCache) AddObject(obj model.Obj) {
	dc.objects[obj.GetName()] = obj
	dc.dirty = true
}

func (dc *DirectoryCache) RemoveObject(name string) {
	delete(dc.objects, name)
	dc.dirty = true
}

func (dc *DirectoryCache) GetObject(name string) (model.Obj, bool) {
	obj, exists := dc.objects[name]
	return obj, exists
}

func (dc *DirectoryCache) GetSortedObjects() []model.Obj {
	if dc.dirty {
		dc.rebuildSortedList()
	}
	return dc.sorted
}

func (dc *DirectoryCache) rebuildSortedList() {
	dc.sorted = dc.sorted[:0]
	for _, obj := range dc.objects {
		dc.sorted = append(dc.sorted, obj)
	}
	dc.dirty = false
}

func (dc *DirectoryCache) Size() int {
	return len(dc.objects)
}
