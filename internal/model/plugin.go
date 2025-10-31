package model

import "time"

// PluginStatus 定义了插件的几种可能状态
type PluginStatus string

const (
	// StatusActive 表示插件已成功加载并正在运行
	StatusActive PluginStatus = "active"
	// StatusInactive 表示插件已安装但未加载（例如，等待重启）
	StatusInactive PluginStatus = "inactive"
	// StatusError 表示插件在加载或运行时遇到错误
	StatusError PluginStatus = "error"
)

type Plugin struct {
	// 插件的唯一标识符，例如 "com.openlist.driver.s3"
	// 这是主键
	ID string `gorm:"primaryKey" json:"id"`

	// --- 来自插件元数据 ---
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	Description string `gorm:"type:text" json:"description"`
	IconURL     string `json:"icon_url"`

	// --- 管理器需要的信息 ---
	// 插件的下载源地址
	SourceURL string `json:"source_url"`
	// Wasm 文件在本地的存储路径
	WasmPath string `json:"wasm_path"`

	// 新增状态字段
	Status  PluginStatus `gorm:"default:'inactive'" json:"status"`
	Message string       `gorm:"type:text" json:"message"` // 用于存储错误信息

	// --- GORM 自动管理字段 ---
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}
