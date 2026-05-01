package cloudflare_imgbed

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Address string `json:"address" type:"text" required:"true" default:"" help:"API domain, https://img.example.com"`
	Token   string `json:"token" type:"text" required:"true" default:"" help:"API authentication token"`
}

var config = driver.Config{
	Name:              "cloudflare_imgbed",
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
