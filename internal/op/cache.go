package op

import (
	stdpath "path"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type CacheManager struct {
	dirCache     *cache.KeyedCache[*directoryCache]       // Cache for directory listings
	linkCache    *cache.TypedCache[*objWithLink]          // Cache for file links
	userCache    *cache.KeyedCache[*model.User]           // Cache for user data
	settingCache *cache.KeyedCache[any]                   // Cache for settings
	detailCache  *cache.KeyedCache[*model.StorageDetails] // Cache for storage details
}

func NewCacheManager() *CacheManager {
	return &CacheManager{
		dirCache:     cache.NewKeyedCache[*directoryCache](time.Minute * 5),
		linkCache:    cache.NewTypedCache[*objWithLink](time.Minute * 30),
		userCache:    cache.NewKeyedCache[*model.User](time.Hour),
		settingCache: cache.NewKeyedCache[any](time.Hour),
		detailCache:  cache.NewKeyedCache[*model.StorageDetails](time.Minute * 30),
	}
}

// global instance
var Cache = NewCacheManager()

func Key(storage driver.Driver, path string) string {
	return stdpath.Join(storage.GetStorage().MountPath, path)
}

func (cm *CacheManager) updateDirectoryObject(storage driver.Driver, dirPath string, oldObj model.Obj, newObj model.Obj) {
	if !oldObj.IsDir() {
		cm.linkCache.DeleteKey(Key(storage, stdpath.Join(dirPath, oldObj.GetName())))
		if oldObj.GetName() != newObj.GetName() {
			cm.linkCache.DeleteKey(Key(storage, stdpath.Join(dirPath, newObj.GetName())))
		}
	}
	if storage.Config().NoCache {
		return
	}
	cache, exist := cm.dirCache.Get(Key(storage, dirPath))
	if exist {
		cache.UpdateObject(oldObj.GetName(), newObj)
	}
}
func (cm *CacheManager) addDirectoryObject(storage driver.Driver, dirPath string, newObj model.Obj) {
	cm.updateDirectoryObject(storage, dirPath, newObj, newObj)
}

func (cm *CacheManager) DeleteDirectoryTree(storage driver.Driver, dirPath string) {
	if storage.Config().NoCache {
		return
	}
	key := Key(storage, dirPath)
	cm.deleteDirectoryTree(key)
}
func (cm *CacheManager) deleteDirectoryTree(key string) {
	if dirCache, exists := cm.dirCache.Take(key); exists {
		for _, obj := range dirCache.objs {
			if obj.IsDir() {
				cm.deleteDirectoryTree(stdpath.Join(key, obj.GetName()))
			}
		}
	}
}

func (cm *CacheManager) DeleteDirectory(storage driver.Driver, dirPath string) {
	if storage.Config().NoCache {
		return
	}
	cm.dirCache.Delete(Key(storage, dirPath))
}
func (cm *CacheManager) removeDirectoryObject(storage driver.Driver, dirPath string, obj model.Obj) {
	if !obj.IsDir() {
		cm.linkCache.DeleteKey(Key(storage, stdpath.Join(dirPath, obj.GetName())))
	}
	if storage.Config().NoCache {
		return
	}
	cache, exist := cm.dirCache.Get(Key(storage, dirPath))
	if exist {
		cache.RemoveObject(obj.GetName())
	}
}

// cache user data
func (cm *CacheManager) SetUser(username string, user *model.User) {
	cm.userCache.Set(username, user)
}

// cached user data
func (cm *CacheManager) GetUser(username string) (*model.User, bool) {
	return cm.userCache.Get(username)
}

// remove user data from cache
func (cm *CacheManager) DeleteUser(username string) {
	cm.userCache.Delete(username)
}

// caches setting
func (cm *CacheManager) SetSetting(key string, setting *model.SettingItem) {
	cm.settingCache.Set(key, setting)
}

// cached setting
func (cm *CacheManager) GetSetting(key string) (*model.SettingItem, bool) {
	if data, exists := cm.settingCache.Get(key); exists {
		if setting, ok := data.(*model.SettingItem); ok {
			return setting, true
		}
	}
	return nil, false
}

// cache setting groups
func (cm *CacheManager) SetSettingGroup(key string, settings []model.SettingItem) {
	cm.settingCache.Set(key, settings)
}

// cached setting group
func (cm *CacheManager) GetSettingGroup(key string) ([]model.SettingItem, bool) {
	if data, exists := cm.settingCache.Get(key); exists {
		if settings, ok := data.([]model.SettingItem); ok {
			return settings, true
		}
	}
	return nil, false
}

func (cm *CacheManager) SetStorageDetails(storage driver.Driver, details *model.StorageDetails) {
	if storage.Config().NoCache {
		return
	}
	expiration := time.Minute * time.Duration(storage.GetStorage().CacheExpiration)
	cm.detailCache.SetWithTTL(storage.GetStorage().MountPath, details, expiration)
}

func (cm *CacheManager) GetStorageDetails(storage driver.Driver) (*model.StorageDetails, bool) {
	return cm.detailCache.Get(storage.GetStorage().MountPath)
}

func (cm *CacheManager) InvalidateStorageDetails(storage driver.Driver) {
	cm.detailCache.Delete(storage.GetStorage().MountPath)
}

// clears all caches
func (cm *CacheManager) ClearAll() {
	cm.dirCache.Clear()
	cm.linkCache.Clear()
	cm.userCache.Clear()
	cm.settingCache.Clear()
	cm.detailCache.Clear()
}

type directoryCache struct {
	objs   []model.Obj
	sorted []model.Obj
	dirty  bool
	mu     sync.RWMutex
}

func newDirectoryCache(objs []model.Obj) *directoryCache {
	sorted := make([]model.Obj, len(objs))
	copy(sorted, objs)
	return &directoryCache{
		objs:   objs,
		sorted: sorted,
	}
}

func (dc *directoryCache) RemoveObject(name string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	for i, obj := range dc.objs {
		if obj.GetName() == name {
			dc.objs = append(dc.objs[:i], dc.objs[i+1:]...)
			dc.dirty = true
			break
		}
	}
}

func (dc *directoryCache) UpdateObject(oldName string, newObj model.Obj) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	for i, obj := range dc.objs {
		if obj.GetName() == oldName {
			dc.objs[i] = newObj
			dc.dirty = true
			return
		}
	}
	dc.objs = append(dc.objs, newObj)
	dc.dirty = true
}

// func (dc *directoryCache) GetObject(name string) (model.Obj, bool) {
// 	dc.mu.RLock()
// 	defer dc.mu.RUnlock()
// 	for _, obj := range dc.objs {
// 		if obj.GetName() == name {
// 			return obj, true
// 		}
// 	}
// 	return nil, false
// }

func (dc *directoryCache) GetSortedObjects(storage driver.Driver) []model.Obj {
	dc.mu.RLock()
	if !dc.dirty {
		dc.mu.RUnlock()
		return dc.sorted
	}
	dc.mu.RUnlock()
	dc.mu.Lock()
	defer dc.mu.Unlock()

	sorted := make([]model.Obj, len(dc.objs))
	copy(sorted, dc.objs)
	dc.sorted = sorted
	if storage.Config().LocalSort {
		model.SortFiles(sorted, storage.GetStorage().OrderBy, storage.GetStorage().OrderDirection)
	}
	model.ExtractFolder(sorted, storage.GetStorage().ExtractFolder)
	dc.dirty = false
	return sorted
}
