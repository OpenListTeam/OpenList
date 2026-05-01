package cfimgbed

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Address string `json:"address" type:"text" required:"true" default:"" help:"API 域名，如 https://img.example.com"`
	Token   string `json:"token" type:"text" required:"true" default:"" help:"API 认证 Token"`
}

var config = driver.Config{
	Name:              "CFImgBed",
	LocalSort:         false,
	OnlyProxy:         false,
	NoCache:           false,
	NoUpload:          true,
	NeedMs:            false,
	DefaultRoot:       "/",
	CheckStatus:       false,
	Alert:             "",
	NoOverwriteUpload: false,
	NoLinkURL:         false,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &CFImgBed{}
	})
}
