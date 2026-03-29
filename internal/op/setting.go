package op

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/pkg/errors"
)

var settingG singleflight.Group[*model.SettingItem]
var settingCacheF = func(item *model.SettingItem) {
	Cache.SetSetting(item.Key, item)
}

var settingGroupG singleflight.Group[[]model.SettingItem]
var settingGroupCacheF = func(key string, items []model.SettingItem) {
	Cache.SetSettingGroup(key, items)
}

var settingChangingCallbacks = make([]func(), 0)

func RegisterSettingChangingCallback(f func()) {
	settingChangingCallbacks = append(settingChangingCallbacks, f)
}

func SettingCacheUpdate() {
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
		_items, err := db.GetSettingItems()
		if err != nil {
			return nil, err
		}
		settingGroupCacheF("ALL_SETTING_ITEMS", _items)
		return _items, nil
	})
	return items, err
}

func GetPublicSettingItems() ([]model.SettingItem, error) {
	if items, exists := Cache.GetSettingGroup("ALL_PUBLIC_SETTING_ITEMS"); exists {
		return items, nil
	}
	items, err, _ := settingGroupG.Do("ALL_PUBLIC_SETTING_ITEMS", func() ([]model.SettingItem, error) {
		_items, err := db.GetPublicSettingItems()
		if err != nil {
			return nil, err
		}
		settingGroupCacheF("ALL_PUBLIC_SETTING_ITEMS", _items)
		return _items, nil
	})
	return items, err
}

func GetSettingItemByKey(key string) (*model.SettingItem, error) {
	if item, exists := Cache.GetSetting(key); exists {
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
	key := fmt.Sprintf("GROUP_%d", group)
	if items, exists := Cache.GetSettingGroup(key); exists {
		return items, nil
	}
	items, err, _ := settingGroupG.Do(key, func() ([]model.SettingItem, error) {
		_items, err := db.GetSettingItemsByGroup(group)
		if err != nil {
			return nil, err
		}
		settingGroupCacheF(key, _items)
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
		_items, err := db.GetSettingItemsInGroups(groups)
		if err != nil {
			return nil, err
		}
		settingGroupCacheF(key, _items)
		return _items, nil
	})
	return items, err
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
