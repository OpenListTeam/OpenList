package cache

import (
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type DirectoryCache struct {
	objs  []model.Obj
	dirty bool
	mu    sync.RWMutex
}

func (dc *DirectoryCache) AddObject(obj model.Obj) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	for i, existObj := range dc.objs {
		if existObj.GetName() == obj.GetName() {
			dc.objs[i] = obj
			dc.dirty = true
			return
		}
	}
	dc.objs = append(dc.objs, obj)
	dc.dirty = true
}

func (dc *DirectoryCache) RemoveObject(name string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	for i, obj := range dc.objs {
		if obj.GetName() == name {
			dc.objs = append(dc.objs[:i], dc.objs[i+1:]...)
			break
		}
	}
	dc.dirty = true
}

func (dc *DirectoryCache) GetObject(name string) (model.Obj, bool) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	for _, obj := range dc.objs {
		if obj.GetName() == name {
			return obj, true
		}
	}
	return nil, false
}

func (dc *DirectoryCache) GetSortedObjects(storage driver.Driver) []model.Obj {
	dc.mu.RLock()
	if !dc.dirty {
		dc.mu.RUnlock()
		return dc.objs
	}
	dc.mu.RUnlock()
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.dirty = false
	if cap(dc.objs) > len(dc.objs) {
		objsCopy := make([]model.Obj, len(dc.objs))
		copy(objsCopy, dc.objs)
		dc.objs = objsCopy
	}
	if storage.Config().LocalSort {
		model.SortFiles(dc.objs, storage.GetStorage().OrderBy, storage.GetStorage().OrderDirection)
	}
	model.ExtractFolder(dc.objs, storage.GetStorage().ExtractFolder)
	return dc.objs
}
