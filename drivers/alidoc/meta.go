package alidoc

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	Cookie string `json:"cookie" type:"text" required:"true" help:"DingTalk AliDoc web cookie"`
}

var config = driver.Config{
	Name:        "AliDoc",
	LocalSort:   true,
	DefaultRoot: "",
	Alert:       "warning|AliDoc uses web cookies captured from the DingTalk document site. Keep the cookie private. This driver is read-only.",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &AliDoc{}
	})
}
