package op

import (
	stdpath "path"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
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
	return utils.GetFullPath(storage.GetStorage().MountPath, path)
}

// recursively delete directory and its children from dirCache
func (cm *CacheManager) DeleteDirectoryTree(storage driver.Driver, dirPath string) {
	if storage.Config().NoCache {
		return
	}
	cm.deleteDirectoryTree(Key(storage, dirPath))
}
func (cm *CacheManager) deleteDirectoryTree(key string) {
	if dirCache, exists := cm.dirCache.Pop(key); exists {
		for _, obj := range dirCache.objs {
			if obj.IsDir() {
				cm.deleteDirectoryTree(stdpath.Join(key, obj.GetName()))
			} else {
				cm.linkCache.DeleteKey(stdpath.Join(key, obj.GetName()))
			}
		}
	}
}

// remove directory from dirCache
func (cm *CacheManager) DeleteDirectory(storage driver.Driver, dirPath string) {
	if storage.Config().NoCache {
		return
	}
	cm.dirCache.Delete(Key(storage, dirPath))
}

// remove object from dirCache.
// if it's a directory, remove all its children from dirCache too.
// if it's a file, remove its link from linkCache.
func (cm *CacheManager) removeDirectoryObject(storage driver.Driver, dirPath string, obj model.Obj) {
	key := Key(storage, dirPath)
	if !obj.IsDir() {
		cm.linkCache.DeleteKey(stdpath.Join(key, obj.GetName()))
	}

	if storage.Config().NoCache {
		return
	}
	if cache, exist := cm.dirCache.Get(key); exist {
		if obj.IsDir() {
			cm.deleteDirectoryTree(stdpath.Join(key, obj.GetName()))
		}
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
	cm.detailCache.SetWithTTL(utils.GetActualMountPath(storage.GetStorage().MountPath), details, expiration)
}

func (cm *CacheManager) GetStorageDetails(storage driver.Driver) (*model.StorageDetails, bool) {
	return cm.detailCache.Get(utils.GetActualMountPath(storage.GetStorage().MountPath))
}

func (cm *CacheManager) InvalidateStorageDetails(storage driver.Driver) {
	cm.detailCache.Delete(utils.GetActualMountPath(storage.GetStorage().MountPath))
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
	mu     sync.RWMutex

	dirtyFlags uint8
}

const (
	dirtyRemove uint8 = 1 << iota // 对象删除：刷新 sorted 副本，但不需要 full sort/extract
	dirtyUpdate                   // 对象更新：需要执行 full sort + extract
)

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
			dc.dirtyFlags |= dirtyRemove
			break
		}
	}
}

func (dc *directoryCache) UpdateObject(oldName string, newObj model.Obj) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if oldName != "" {
		for i, obj := range dc.objs {
			if obj.GetName() == oldName {
				dc.objs[i] = newObj
				dc.dirtyFlags |= dirtyUpdate
				return
			}
		}
	}
	dc.objs = append(dc.objs, newObj)
	dc.dirtyFlags |= dirtyUpdate
}

func (dc *directoryCache) GetSortedObjects(meta driver.Meta) []model.Obj {
	dc.mu.RLock()
	if dc.dirtyFlags == 0 {
		dc.mu.RUnlock()
		return dc.sorted
	}
	dc.mu.RUnlock()
	dc.mu.Lock()
	defer dc.mu.Unlock()

	sorted := make([]model.Obj, len(dc.objs))
	copy(sorted, dc.objs)
	dc.sorted = sorted
	if dc.dirtyFlags&dirtyUpdate != 0 {
		storage := meta.GetStorage()
		if meta.Config().LocalSort {
			model.SortFiles(sorted, storage.OrderBy, storage.OrderDirection)
		}
		model.ExtractFolder(sorted, storage.ExtractFolder)
	}
	dc.dirtyFlags = 0
	return sorted
}
