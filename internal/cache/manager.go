package cache

import (
	"path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type CacheManager struct {
	directories    *KeyedCache // Cache for directory listings
	links          *TypedCache // Cache for file links
	users          *KeyedCache // Cache for user data
	settings       *KeyedCache // Cache for settings
	storageDetails *KeyedCache // Cache for storage details
}

func NewCacheManager() *CacheManager {
	return &CacheManager{
		directories:    NewKeyedCache(time.Minute * 5),
		links:          NewTypedCache(time.Minute * 30),
		users:          NewKeyedCache(time.Hour),
		settings:       NewKeyedCache(time.Hour),
		storageDetails: NewKeyedCache(time.Minute * 30),
	}
}

// global instance
var Manager = NewCacheManager()

// creates consistent cache key for directory operations
func makeDirectoryKey(storage driver.Driver, dirPath string) string {
	return path.Join(storage.GetStorage().MountPath, utils.FixAndCleanPath(dirPath))
}

// cached directory listing
func (cm *CacheManager) GetDirectoryListing(storage driver.Driver, dirPath string) (*DirectoryCache, bool) {
	key := makeDirectoryKey(storage, dirPath)
	if data, exists := cm.directories.Get(key); exists {
		if dirCache, ok := data.(*DirectoryCache); ok {
			return dirCache, true
		}
	}
	return nil, false
}

// cache a directory listing
func (cm *CacheManager) SetDirectoryListing(storage driver.Driver, dirPath string, objects []model.Obj) {
	if storage.Config().NoCache {
		return
	}

	key := makeDirectoryKey(storage, dirPath)
	dirCache := NewDirectoryCache()
	for _, obj := range objects {
		dirCache.AddObject(obj)
	}
	expiration := time.Minute * time.Duration(storage.GetStorage().CacheExpiration)
	cm.directories.SetWithTTL(key, dirCache, expiration)
}

// update a obj in a cached directory
func (cm *CacheManager) UpdateDirectoryObject(storage driver.Driver, dirPath string, obj model.Obj) {
	key := makeDirectoryKey(storage, dirPath)
	if data, exists := cm.directories.Get(key); exists {
		if dirCache, ok := data.(*DirectoryCache); ok {
			dirCache.AddObject(obj)
		}
	}
}

// remove an object from a cached directory
func (cm *CacheManager) RemoveDirectoryObject(storage driver.Driver, dirPath string, objName string) {
	key := makeDirectoryKey(storage, dirPath)
	if data, exists := cm.directories.Get(key); exists {
		if dirCache, ok := data.(*DirectoryCache); ok {
			dirCache.RemoveObject(objName)
		}
	}
}

// remove a directory from the cache
func (cm *CacheManager) InvalidateDirectory(storage driver.Driver, dirPath string) {
	key := makeDirectoryKey(storage, dirPath)
	cm.directories.Delete(key)
}

func (cm *CacheManager) InvalidateDirectoryTree(storage driver.Driver, dirPath string) {
	if dirCache, exists := cm.GetDirectoryListing(storage, dirPath); exists {
		for _, obj := range dirCache.GetSortedObjects() {
			if obj.IsDir() {
				subPath := path.Join(dirPath, obj.GetName())
				cm.InvalidateDirectoryTree(storage, subPath)
			}
		}
	}
	cm.InvalidateDirectory(storage, dirPath)
}

// cache a file link
func (cm *CacheManager) SetLink(key, typo string, link *model.Link, expiration time.Duration) {
	cm.links.SetTypeWithTTL(key, typo, link, expiration)
}

// cached file link
func (cm *CacheManager) GetLink(key, typo string) (*model.Link, bool) {
	if data, exists := cm.links.GetType(key, typo); exists {
		if link, ok := data.(*model.Link); ok {
			return link, true
		}
	}
	return nil, false
}

// remove a specific link from cache
func (cm *CacheManager) InvalidateLink(key string) {
	cm.links.DeleteKey(key)
}

// remove a specific link from cache
func (cm *CacheManager) DelLink(key string) {
	cm.links.DeleteKey(key)
}

// cache user data
func (cm *CacheManager) SetUser(username string, user *model.User) {
	cm.users.Set(username, user)
}

// cached user data
func (cm *CacheManager) GetUser(username string) (*model.User, bool) {
	if data, exists := cm.users.Get(username); exists {
		if user, ok := data.(*model.User); ok {
			return user, true
		}
	}
	return nil, false
}

// remove user data from cache
func (cm *CacheManager) InvalidateUser(username string) {
	cm.users.Delete(username)
}

// caches setting
func (cm *CacheManager) SetSetting(key string, setting *model.SettingItem) {
	cm.settings.Set(key, setting)
}

// cached setting
func (cm *CacheManager) GetSetting(key string) (*model.SettingItem, bool) {
	if data, exists := cm.settings.Get(key); exists {
		if setting, ok := data.(*model.SettingItem); ok {
			return setting, true
		}
	}
	return nil, false
}

// cache setting groups
func (cm *CacheManager) SetSettingGroup(key string, settings []model.SettingItem) {
	cm.settings.Set(key, settings)
}

// cached setting group
func (cm *CacheManager) GetSettingGroup(key string) ([]model.SettingItem, bool) {
	if data, exists := cm.settings.Get(key); exists {
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
	cm.storageDetails.SetWithTTL(storage.GetStorage().MountPath, details, expiration)
}

func (cm *CacheManager) GetStorageDetails(storage driver.Driver) (*model.StorageDetails, bool) {
	if data, exists := cm.storageDetails.Get(storage.GetStorage().MountPath); exists {
		if details, ok := data.(*model.StorageDetails); ok {
			return details, true
		}
	}
	return nil, false
}

func (cm *CacheManager) InvalidateStorageDetails(storage driver.Driver) {
	cm.storageDetails.Delete(storage.GetStorage().MountPath)
}

// clears all caches
func (cm *CacheManager) ClearAll() {
	cm.directories.Clear()
	cm.links.Clear()
	cm.users.Clear()
	cm.settings.Clear()
	cm.storageDetails.Clear()
}
