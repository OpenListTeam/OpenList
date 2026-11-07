package op

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/pkg/errors"
)

var settingG singleflight.Group[*model.SettingItem]
var settingGroupG singleflight.Group[[]model.SettingItem]

// settingCacheMu guards the generation counter that makes a cache fill
// conditional. A reader that missed the cache fetches from the database and
// then fills the cache; an admin save in between may land its ClearAll first,
// and the fill would then put the value the reader fetched *before* the save
// back into a cache that has no expiry - the old value survives indefinitely.
// Each fill carries the generation it observed before reading and is skipped
// when a save has bumped it since.
var (
	settingCacheMu  sync.Mutex
	settingCacheGen uint64
)

func settingCacheGeneration() uint64 {
	settingCacheMu.Lock()
	defer settingCacheMu.Unlock()
	return settingCacheGen
}

// settingCacheF stores an item unless a save superseded the read it came from.
func settingCacheF(gen uint64, item *model.SettingItem) {
	settingCacheMu.Lock()
	defer settingCacheMu.Unlock()
	if gen != settingCacheGen {
		return
	}
	Cache.SetSetting(item.Key, item)
}

// settingGroupCacheF is the grouped counterpart of settingCacheF.
func settingGroupCacheF(gen uint64, key string, items []model.SettingItem) {
	settingCacheMu.Lock()
	defer settingCacheMu.Unlock()
	if gen != settingCacheGen {
		return
	}
	Cache.SetSettingGroup(key, items)
}

var settingChangingCallbacks = make([]func(), 0)

func RegisterSettingChangingCallback(f func()) {
	settingChangingCallbacks = append(settingChangingCallbacks, f)
}

func SettingCacheUpdate() {
	settingCacheMu.Lock()
	settingCacheGen++
	settingCacheMu.Unlock()
	Cache.ClearAll()
	for _, cb := range settingChangingCallbacks {
		cb()
	}
}

func GetPublicSettingsMap() map[string]string {
	items, _ := GetPublicSettingItems()
	pSettings := make(map[string]string)
	for _, item := range items {
		pSettings[item.Key] = item.Value
	}
	return pSettings
}

func GetSettingsMap() map[string]string {
	items, _ := GetSettingItems()
	settings := make(map[string]string)
	for _, item := range items {
		settings[item.Key] = item.Value
	}
	return settings
}

func GetSettingItems() ([]model.SettingItem, error) {
	if items, exists := Cache.GetSettingGroup("ALL_SETTING_ITEMS"); exists {
		return items, nil
	}
	items, err, _ := settingGroupG.Do("ALL_SETTING_ITEMS", func() ([]model.SettingItem, error) {
		gen := settingCacheGeneration()
		_items, err := db.GetSettingItems()
		if err != nil {
			return nil, err
		}
		settingGroupCacheF(gen, "ALL_SETTING_ITEMS", _items)
		return _items, nil
	})
	return items, err
}

// sensitiveSettingKeys are never published to anonymous clients, whatever row
// a client writes. A save payload that omits `flag` carries the zero value,
// which is PUBLIC, so an unguarded save published the S3 master key through
// /api/public/settings.
var sensitiveSettingKeys = map[string]bool{
	conf.S3AccessKeyId:     true,
	conf.S3SecretAccessKey: true,
	conf.S3BucketAuth:      true,
	conf.S3Buckets:         true,
	conf.S3BucketCors:      true,
}

// keepSensitiveSettingPrivate restores the metadata a partial save payload
// dropped and pins sensitive rows to PRIVATE.
func keepSensitiveSettingPrivate(item *model.SettingItem) {
	if !sensitiveSettingKeys[item.Key] {
		return
	}
	if old, err := db.GetSettingItemByKey(item.Key); err == nil && old != nil {
		// An omitted field is the zero value; the stored row is the better
		// source for presentation metadata the client did not send.
		if item.Group == 0 && old.Group != 0 {
			item.Group = old.Group
		}
		if item.Type == "" {
			item.Type = old.Type
		}
		if item.Index == 0 && old.Index != 0 {
			item.Index = old.Index
		}
		if item.Help == "" {
			item.Help = old.Help
		}
	}
	item.Flag = model.PRIVATE
}

func GetPublicSettingItems() ([]model.SettingItem, error) {
	if items, exists := Cache.GetSettingGroup("ALL_PUBLIC_SETTING_ITEMS"); exists {
		return items, nil
	}
	items, err, _ := settingGroupG.Do("ALL_PUBLIC_SETTING_ITEMS", func() ([]model.SettingItem, error) {
		gen := settingCacheGeneration()
		_items, err := db.GetPublicSettingItems()
		if err != nil {
			return nil, err
		}
		// Belt and braces: a row that somehow reached PUBLIC is still not
		// published while it holds a credential or a bucket policy.
		kept := _items[:0]
		for _, item := range _items {
			if sensitiveSettingKeys[item.Key] {
				continue
			}
			kept = append(kept, item)
		}
		_items = kept
		settingGroupCacheF(gen, "ALL_PUBLIC_SETTING_ITEMS", _items)
		return _items, nil
	})
	return items, err
}

func GetSettingItemByKey(key string) (*model.SettingItem, error) {
	if item, exists := Cache.GetSetting(key); exists {
		return item, nil
	}

	item, err, _ := settingG.Do(key, func() (*model.SettingItem, error) {
		gen := settingCacheGeneration()
		_item, err := db.GetSettingItemByKey(key)
		if err != nil {
			return nil, err
		}
		settingCacheF(gen, _item)
		return _item, nil
	})
	return item, err
}

func GetSettingItemInKeys(keys []string) ([]model.SettingItem, error) {
	var items []model.SettingItem
	for _, key := range keys {
		item, err := GetSettingItemByKey(key)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, nil
}

func GetSettingItemsByGroup(group int) ([]model.SettingItem, error) {
	key := fmt.Sprintf("GROUP_%d", group)
	if items, exists := Cache.GetSettingGroup(key); exists {
		return items, nil
	}
	items, err, _ := settingGroupG.Do(key, func() ([]model.SettingItem, error) {
		gen := settingCacheGeneration()
		_items, err := db.GetSettingItemsByGroup(group)
		if err != nil {
			return nil, err
		}
		settingGroupCacheF(gen, key, _items)
		return _items, nil
	})
	return items, err
}

func GetSettingItemsInGroups(groups []int) ([]model.SettingItem, error) {
	sort.Ints(groups)

	keyParts := make([]string, 0, len(groups))
	for _, g := range groups {
		keyParts = append(keyParts, strconv.Itoa(g))
	}
	key := "GROUPS_" + strings.Join(keyParts, "_")

	if items, exists := Cache.GetSettingGroup(key); exists {
		return items, nil
	}
	items, err, _ := settingGroupG.Do(key, func() ([]model.SettingItem, error) {
		gen := settingCacheGeneration()
		_items, err := db.GetSettingItemsInGroups(groups)
		if err != nil {
			return nil, err
		}
		settingGroupCacheF(gen, key, _items)
		return _items, nil
	})
	return items, err
}

func SaveSettingItems(items []model.SettingItem) error {
	for i := range items {
		item := &items[i]
		keepSensitiveSettingPrivate(item)
		if it, ok := MigrationSettingItems[item.Key]; ok &&
			item.Value == it.MigrationValue {
			item.Value = it.Value
		}
		if ok, err := HandleSettingItemHook(item); ok && err != nil {
			return fmt.Errorf("failed to execute hook on %s: %+v", item.Key, err)
		}
	}
	err := db.SaveSettingItems(items)
	if err != nil {
		return fmt.Errorf("failed save setting: %+v", err)
	}
	SettingCacheUpdate()
	return nil
}

func SaveSettingItem(item *model.SettingItem) (err error) {
	keepSensitiveSettingPrivate(item)
	if it, ok := MigrationSettingItems[item.Key]; ok &&
		item.Value == it.MigrationValue {
		item.Value = it.Value
	}
	// hook
	if _, err := HandleSettingItemHook(item); err != nil {
		return fmt.Errorf("failed to execute hook on %s: %+v", item.Key, err)
	}
	// update
	if err = db.SaveSettingItem(item); err != nil {
		return fmt.Errorf("failed save setting on %s: %+v", item.Key, err)
	}
	SettingCacheUpdate()
	return nil
}

// SaveSettingItemQuiet persists one setting and refreshes only the setting
// caches. SettingCacheUpdate() is the right response to an admin edit, but the
// S3 gateway also writes settings (per-bucket CORS) on behalf of S3 clients: a
// single client looping `PUT /bucket?cors` would otherwise drop the directory,
// link, user and detail caches and fire every change callback on each request.
func SaveSettingItemQuiet(item *model.SettingItem) (err error) {
	keepSensitiveSettingPrivate(item)
	if it, ok := MigrationSettingItems[item.Key]; ok &&
		item.Value == it.MigrationValue {
		item.Value = it.Value
	}
	if _, err := HandleSettingItemHook(item); err != nil {
		return fmt.Errorf("failed to execute hook on %s: %+v", item.Key, err)
	}
	if err = db.SaveSettingItem(item); err != nil {
		return fmt.Errorf("failed save setting on %s: %+v", item.Key, err)
	}
	settingCacheF(settingCacheGeneration(), item)
	Cache.DeleteSettingGroup("ALL_SETTING_ITEMS")
	Cache.DeleteSettingGroup("ALL_PUBLIC_SETTING_ITEMS")
	return nil
}

func DeleteSettingItemByKey(key string) error {
	old, err := GetSettingItemByKey(key)
	if err != nil {
		return errors.WithMessage(err, "failed to get settingItem")
	}
	if !old.IsDeprecated() {
		return errors.Errorf("setting [%s] is not deprecated", key)
	}
	SettingCacheUpdate()
	return db.DeleteSettingItemByKey(key)
}

type MigrationValueItem struct {
	MigrationValue, Value string
}

var MigrationSettingItems map[string]MigrationValueItem
