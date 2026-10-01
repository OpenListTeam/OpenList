package filen

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	Email       string `json:"email" required:"true" help:"Filen 账号邮箱"`
	Password    string `json:"password" required:"true" help:"Filen 账号密码（仅在本地派生加密密钥，不会发送到服务器）"`
	TwoFASecret string `json:"two_fa_secret" help:"2FA 密钥（TOTP secret，用于自动生成动态验证码，开启了两步验证的账号需要填写）"`
	APIKey      string `json:"api_key" help:"Filen API Key，留空则自动登录获取并回填（可通过 Filen CLI 的 export-api-key 命令获取）"`
}

var config = driver.Config{
	Name:              "Filen",
	LocalSort:         true,
	DefaultRoot:       "",
	NoOverwriteUpload: false,
	// Filen 端到端加密，直链返回密文，必须由服务端解密中转：
	// OnlyProxy 暴露给前端，限制 WebDAV 策略只能选本地代理；
	// NoLinkURL 供后端判断 Link 无 URL，强制走代理下载。
	OnlyProxy: true,
	NoLinkURL: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Filen{}
	})
}
