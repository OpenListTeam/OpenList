package alidoc

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	Cookie       string `json:"cookie" type:"text" required:"true" help:"钉钉文档网页 Cookie"`
}

var config = driver.Config{
	Name:        "AliDoc",
	LocalSort:   true,
	DefaultRoot: "",
	Alert:       "info|This driver supports accessing DingDrive through DingTalk Docs and is currently read-only.",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &AliDoc{}
	})
}
