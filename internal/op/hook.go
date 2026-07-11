package op

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// Obj
type ObjsUpdateHook = func(ctx context.Context, parent string, objs []model.Obj)

var (
	objsUpdateHooks = make([]ObjsUpdateHook, 0)
)

func RegisterObjsUpdateHook(hook ObjsUpdateHook) {
	objsUpdateHooks = append(objsUpdateHooks, hook)
}

func HandleObjsUpdateHook(ctx context.Context, parent string, objs []model.Obj) {
	for _, hook := range objsUpdateHooks {
		hook(ctx, parent, objs)
	}
}

// Setting
type SettingItemHook func(item *model.SettingItem) error

var settingItemHooks = map[string]SettingItemHook{
	conf.VideoTypes: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.VideoTypes] = strings.Split(item.Value, ",")
		return nil
	},
	conf.AudioTypes: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.AudioTypes] = strings.Split(item.Value, ",")
		return nil
	},
	conf.ImageTypes: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.ImageTypes] = strings.Split(item.Value, ",")
		return nil
	},
	conf.TextTypes: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.TextTypes] = strings.Split(item.Value, ",")
		return nil
	},
	conf.ProxyTypes: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.ProxyTypes] = strings.Split(item.Value, ",")
		return nil
	},
	conf.ProxyIgnoreHeaders: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.ProxyIgnoreHeaders] = strings.Split(item.Value, ",")
		return nil
	},
	conf.PrivacyRegs: func(item *model.SettingItem) error {
		regStrs := strings.Split(item.Value, "\n")
		regs := make([]*regexp.Regexp, 0, len(regStrs))
		for _, regStr := range regStrs {
			reg, err := regexp.Compile(regStr)
			if err != nil {
				return errors.WithStack(err)
			}
			regs = append(regs, reg)
		}
		conf.PrivacyReg = regs
		return nil
	},
	conf.FilenameCharMapping: func(item *model.SettingItem) error {
		err := utils.Json.UnmarshalFromString(item.Value, &conf.FilenameCharMap)
		if err != nil {
			return err
		}
		log.Debugf("filename char mapping: %+v", conf.FilenameCharMap)
		return nil
	},
	conf.IgnoreDirectLinkParams: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.IgnoreDirectLinkParams] = strings.Split(item.Value, ",")
		return nil
	},
	conf.MultipartChunkSize: func(item *model.SettingItem) error {
		size, err := strconv.Atoi(strings.TrimSpace(item.Value))
		if err != nil || size < 1 {
			// deliberately a plain error: SaveSettings formats hook errors
			// with %+v, which would dump a full stack trace into the UI
			// notification for stack-carrying errors
			return fmt.Errorf("multipart chunk size must be a positive integer (MB), got %q", item.Value)
		}
		item.Value = strconv.Itoa(size)
		return nil
	},
}

func RegisterSettingItemHook(key string, hook SettingItemHook) {
	settingItemHooks[key] = hook
}

func HandleSettingItemHook(item *model.SettingItem) (hasHook bool, err error) {
	if hook, ok := settingItemHooks[item.Key]; ok {
		return true, hook(item)
	}
	return false, nil
}

// Storage
type StorageHook func(typ string, storage driver.Driver)

var storageHooks = make([]StorageHook, 0)

func callStorageHooks(typ string, storage driver.Driver) {
	for _, hook := range storageHooks {
		hook(typ, storage)
	}
}

func RegisterStorageHook(hook StorageHook) {
	storageHooks = append(storageHooks, hook)
}
