package pikpak

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	Username         string `json:"username" required:"true"`
	Password         string `json:"password" required:"true"`
	Platform         string `json:"platform" ignore:"true" default:""`
	RefreshToken     string `json:"refresh_token" ignore:"true" default:""`
	DeviceID         string `json:"device_id" ignore:"true" default:""`
	DisableMediaLink bool   `json:"disable_media_link" default:"true"`
}

var config = driver.Config{
	Name:        "PikPak",
	LocalSort:   true,
	PreferProxy: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &PikPak{}
	})
}
