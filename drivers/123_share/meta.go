package _123Share

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	ShareKey string `json:"sharekey" required:"true"`
	SharePwd string `json:"sharepassword"`
	driver.RootID
	//OrderBy        string `json:"order_by" type:"select" options:"file_name,size,update_at" default:"file_name"`
	//OrderDirection string `json:"order_direction" type:"select" options:"asc,desc" default:"asc"`
	AccessToken string `json:"accesstoken" type:"text"`
}

var config = driver.Config{
	Name:        "123PanShare",
	LocalSort:   true,
	NoUpload:    true,
	DefaultRoot: "0",
	PreferProxy: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Pan123Share{}
	})
}
