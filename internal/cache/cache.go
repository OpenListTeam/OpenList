package cache

import (
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

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
