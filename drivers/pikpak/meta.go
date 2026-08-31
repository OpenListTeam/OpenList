package pikpak

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	Username         string `json:"username" required:"true"`
	Password         string `json:"password" required:"true"`
	Platform         string `json:"platform" required:"true" default:"web" type:"select" options:"android,web,pc"`
	RefreshToken     string `json:"refresh_token" required:"false" default:""`
	CaptchaToken     string `json:"captcha_token" default:""`
	DeviceID         string `json:"device_id"  required:"false" default:""`
	DisableMediaLink bool   `json:"disable_media_link" default:"true"`
	SkipVerification bool   `json:"skip_verification" default:"true"`
}

var config = driver.Config{
	Name:      "PikPak",
	LocalSort: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &PikPak{}
	})
}
