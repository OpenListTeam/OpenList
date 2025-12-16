package wps

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Cookie string `json:"cookie" required:"true" type:"text"`
	Mode   string `json:"mode" type:"select" options:"Personal,Business" default:"Business"`
}

var config = driver.Config{
	Name:              "WPS",
	LocalSort:         true,
	DefaultRoot:       "/",
	Alert:             "",
	NoOverwriteUpload: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Wps{}
	})
}
