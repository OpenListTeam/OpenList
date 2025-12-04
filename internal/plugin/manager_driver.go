package plugin

import (
	"context"
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	log "github.com/sirupsen/logrus"
)

// --- 驱动插件处理器 ---

// DriverPluginHandler 实现了 PluginHandler 接口，专门处理驱动插件
type DriverPluginHandler struct{}

func (h *DriverPluginHandler) Prefix() string {
	return "openlist.driver."
}

func (h *DriverPluginHandler) Register(ctx context.Context, plugin *PluginInfo) error {
	if plugin.driver != nil {
		return nil // 已经注册过了
	}

	var err error
	plugin.driver, err = NewDriverPlugin(ctx, plugin)
	if err != nil {
		return fmt.Errorf("load driver plugin err: %w", err)
	}

	err = op.RegisterDriver(func() driver.Driver {
		tempDriver, err := plugin.driver.NewWasmDriver()
		if err != nil {
			log.Errorf("deferred load driver plugin err: %v", err)
			return nil
		}
		return tempDriver
	})
	if err != nil {
		// 如果注册失败，关闭运行时
		plugin.driver.Close(ctx)
		return fmt.Errorf("failed to register driver in op: %w", err)
	}

	log.Infof("Successfully registered driver for plugin: %s", plugin.ID)
	return nil
}

func (h *DriverPluginHandler) Unregister(ctx context.Context, plugin *PluginInfo) error {
	if plugin.driver == nil {
		log.Errorf("plugin.driver is nil during unregister for plugin '%s', cannot get config", plugin.ID)
		return fmt.Errorf("plugin.driver instance not found, cannot properly unregister from op")
	}

	op.UnRegisterDriver(func() driver.Driver {
		tempDriver, err := plugin.driver.NewWasmDriver()
		if err != nil {
			log.Warnf("Failed to create temp driver for unregister: %v", err)
			return nil
		}
		return tempDriver
	})

	if err := plugin.driver.Close(ctx); err != nil {
		log.Warnf("Error closing driver plugin runtime for %s: %v", plugin.ID, err)
	}

	return nil
}
