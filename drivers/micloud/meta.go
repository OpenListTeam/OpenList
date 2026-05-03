package micloud

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID        // 使用RootID作为根目录标识
	UserId        string `json:"user_id" required:"true" help:"小米云盘用户ID"`
	ServiceToken  string `json:"service_token" required:"true" help:"小米云盘服务令牌"`
	DeviceId      string `json:"device_id" required:"true" help:"设备ID"`
}

var config = driver.Config{
	Name:          "MiCloud",
	LocalSort:     true, // 本地排序
	OnlyLinkMFile: false,
	OnlyProxy:     false, // 允许直链
	NoCache:       false,
	NoUpload:      false, // 支持上传
	NeedMs:        false,
	DefaultRoot:   "0",  // 根目录ID为0
	CheckStatus:   true, // 检查状态
	//Alert:             "注意：需要提供正确的用户ID、服务令牌和设备ID",
	NoOverwriteUpload: false,
	NoLinkURL:         false, // Link 返回直链 URL
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &MiCloud{}
	})
}
