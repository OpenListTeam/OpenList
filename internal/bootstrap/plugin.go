// internal/bootstrap/plugin.go
package bootstrap

import (
	"context"
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/internal/plugin"
)

// InitPlugins 初始化插件管理器
func InitPlugins() {
	// 2. 创建并初始化 Manager
	// "data" 目录应从配置中获取
	manager, err := plugin.NewManager(context.Background(), flags.DataDir)
	if err != nil {
		// 在启动时，如果插件系统失败，应该 panic
		panic(fmt.Sprintf("Failed to initialize plugin manager: %v", err))
	}

	plugin.PluginManager = manager
}
