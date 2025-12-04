package db

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/gorm"
)

// CreatePlugin 在数据库中插入一条新的插件记录
// 如果记录已存在，则会更新它 (Upsert)
func CreatePlugin(ctx context.Context, plugin *model.Plugin) error {
	return db.WithContext(ctx).Save(plugin).Error
}

// GetPluginByID 从数据库中根据 ID 查询单个插件
func GetPluginByID(ctx context.Context, id string) (*model.Plugin, error) {
	var plugin model.Plugin
	err := db.WithContext(ctx).First(&plugin, "id = ?", id).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil // 返回 nil, nil 表示未找到
		}
		return nil, err
	}
	return &plugin, nil
}

// GetAllPlugins 从数据库中获取所有已安装的插件
func GetAllPlugins(ctx context.Context) ([]*model.Plugin, error) {
	var plugins []*model.Plugin
	err := db.WithContext(ctx).Find(&plugins).Error
	return plugins, err
}

// DeletePluginByID 从数据库中根据 ID 删除一个插件
func DeletePluginByID(ctx context.Context, id string) error {
	return db.WithContext(ctx).Delete(&model.Plugin{}, "id = ?", id).Error
}

// UpdatePluginStatus 更新指定插件的状态和消息
func UpdatePluginStatus(ctx context.Context, pluginID string, status model.PluginStatus, message string) error {
	return db.WithContext(ctx).Model(&model.Plugin{}).Where("id = ?", pluginID).Updates(map[string]interface{}{
		"status":  status,
		"message": message,
	}).Error
}
