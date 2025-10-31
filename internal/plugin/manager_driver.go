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
		driver, err := plugin.driver.NewWasmDriver()
		if err != nil {
			log.Errorf("deferred load driver plugin err: %v", err)
		}
		return driver
	})
	if err != nil {
		return fmt.Errorf("failed to register driver in op: %w", err)
	}

	log.Infof("Successfully registered driver for plugin: %s", plugin.ID)
	return nil
}

func (h *DriverPluginHandler) Unregister(ctx context.Context, plugin *PluginInfo) error {
	// 遵循用户提供的模式，传递一个工厂函数来注销
	op.UnRegisterDriver(func() driver.Driver {
		if plugin.driver == nil {
			// 如果 driver 实例不存在，尝试临时创建一个用于获取元数据
			// 注意：这可能因插件的复杂性而失败
			tempDriver, err := NewDriverPlugin(ctx, plugin)
			if err != nil {
				log.Errorf("failed to create temporary driver for unregistering plugin '%s': %v", plugin.ID, err)
				return nil
			}
			d, _ := tempDriver.NewWasmDriver()
			return d
		}
		d, _ := plugin.driver.NewWasmDriver()
		return d
	})
	return nil
}
