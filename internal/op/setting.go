package op

import (
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/pkg/errors"
)

var settingG singleflight.Group[*model.SettingItem]
var settingCacheF = func(item *model.SettingItem) {
	cache.Manager.SetSetting(item.Key, item)
}

var settingGroupG singleflight.Group[[]model.SettingItem]

var settingChangingCallbacks = make([]func(), 0)

func RegisterSettingChangingCallback(f func()) {
	settingChangingCallbacks = append(settingChangingCallbacks, f)
}

func SettingCacheUpdate() {
	cache.Manager.ClearAll()
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
	return db.GetSettingItems()
}

func GetPublicSettingItems() ([]model.SettingItem, error) {
	return db.GetPublicSettingItems()
}

func GetSettingItemByKey(key string) (*model.SettingItem, error) {
	if item, exists := cache.Manager.GetSetting(key); exists {
		return item, nil
	}

	item, err, _ := settingG.Do(key, func() (*model.SettingItem, error) {
		_item, err := db.GetSettingItemByKey(key)
		if err != nil {
			return nil, err
		}
		settingCacheF(_item)
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
	return db.GetSettingItemsByGroup(group)
}

func GetSettingItemsInGroups(groups []int) ([]model.SettingItem, error) {
	return db.GetSettingItemsInGroups(groups)
}

func SaveSettingItems(items []model.SettingItem) error {
	for i := range items {
		item := &items[i]
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
