package template

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	Username string `json:"username" required:"true" label:"Degoo用户名" help:"您的Degoo账户邮箱"`
	Password string `json:"password" type:"password" required:"true" label:"Degoo密码" help:"您的Degoo账户密码"`
	// Python脚本中的API密钥，我们可以将其硬编码在这里或作为可选配置项
	// APIKey string `json:"apiKey" type:"password" label:"API密钥" default:"da2-vs6twz5vnjdavpqndtbzg3prra"`
}

var config = driver.Config{
	Name:              "Degoo",
	LocalSort:         false,
	OnlyLinkMFile:     false,
	OnlyProxy:         false,
	NoCache:           false,
	NoUpload:          false,
	NeedMs:            true,
	DefaultRoot:       "0", // Python脚本中根目录ID为0
	CheckStatus:       true,
	Alert:             "",
	NoOverwriteUpload: false,
	NoLinkURL:         false,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Degoo{}
	})
}
