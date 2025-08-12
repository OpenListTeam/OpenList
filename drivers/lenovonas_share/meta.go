package LenovoNasShare

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	ShareId        string `json:"share_id" required:"true" help:"The part after the last / in the shared link"`
	SharePwd       string `json:"share_pwd" required:"true" help:"The password of the shared link"`
	Host           string `json:"host" required:"true" default:"https://siot-share.lenovo.com.cn" help:"You can change it to your local area network"`
	ShowRootFolder bool   `json:"show_root_folder" default:"true"`
}

var config = driver.Config{
	Name:      "LenovoNasShare",
	LocalSort: true,
	NoUpload:  true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &LenovoNasShare{}
	})
}
