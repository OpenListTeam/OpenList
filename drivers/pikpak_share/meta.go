package pikpak_share

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	driver.RootID
	ShareId               string `json:"share_id" required:"true"`
	SharePwd              string `json:"share_pwd"`
	Platform              string `json:"platform" default:"web" required:"true" type:"select" options:"android,web,pc"`
	DeviceID              string `json:"device_id"  required:"false" default:""`
	UseTransCodingAddress bool   `json:"use_transcoding_address" required:"true" default:"false"`
}

var config = driver.Config{
	Name:      "PikPakShare",
	LocalSort: true,
	NoUpload:  true,
}

func New() driver.Driver {
	return &PikPakShare{}
}
