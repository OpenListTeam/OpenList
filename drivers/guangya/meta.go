package guangya

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	RootPath       string `json:"root_path" help:"光鸭云盘中的完整路径"`
	PhoneNumber    string `json:"phone_number" type:"text" help:"手机号，如 +86 13800000000"`
	CaptchaToken   string `json:"captcha_token" type:"text" help:"验证码令牌，用于 /v1/auth/verification"`
	SendCode       bool   `json:"send_code" type:"bool" help:"设为 true 并保存以发送短信验证码，发送后自动重置为 false"`
	VerifyCode     string `json:"verify_code" type:"text" help:"短信验证码，与 phone_number 一起填写后保存以完成登录"`
	VerificationID string `json:"verification_id" type:"text" help:"发送短信后自动生成，请勿手动编辑"`
	AccessToken    string `json:"access_token" type:"text" help:"Bearer access token（如有 refresh_token 则可选）"`
	RefreshToken   string `json:"refresh_token" type:"text" help:"刷新令牌，用于自动登录/自动续期"`
	ClientID       string `json:"client_id" default:"aMe-8VSlkrbQXpUR"`
	DeviceID       string `json:"device_id" help:"可选自定义设备 ID（32 位十六进制），为空则自动生成"`
	PageSize       int    `json:"page_size" type:"number" default:"100"`
	OrderBy        int    `json:"order_by" type:"number" options:"0,1,2,3,4" default:"3"`
	SortType       int    `json:"sort_type" type:"number" options:"0,1" default:"1"`
}

var config = driver.Config{
	Name:              "GuangYaPan",
	LocalSort:         false,
	OnlyProxy:         false,
	NoCache:           false,
	NoUpload:          false,
	NeedMs:            false,
	DefaultRoot:       "",
	CheckStatus:       true,
	Alert:             "info|两阶段短信登录：(1) 填写 phone_number，设置 send_code=true 并保存；(2) 填写 verify_code 并保存以完成登录。",
	NoOverwriteUpload: true,
	NoLinkURL:         false,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Guangya{}
	})
}
