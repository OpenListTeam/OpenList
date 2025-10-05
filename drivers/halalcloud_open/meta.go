package halalcloudopen

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	// Usually one of two
	driver.RootPath
	// define other
	RefreshToken string `json:"refresh_token" required:"true" help:"login type is refresh_token,this is required"`
	UploadThread string `json:"upload_thread" default:"3" help:"1 <= thread <= 32"`

	ClientID     string `json:"client_id" required:"true" default:""`
	AppVersion   string `json:"app_version" required:"true" default:"1.0.0"`
	ClientSecret string `json:"client_secret" required:"true" default:""`
	UserAgent    string `json:"user_agent" required:"true" default:"OpenList/1.0.0"`
	Host         string `json:"host" required:"true" default:"openapi.2dland.cn"`
	TimeOut      int    `json:"timeout" default:"60" help:"timeout in seconds"`
}

var config = driver.Config{
	Name:        "HalalCloudOpen",
	OnlyProxy:   false,
	DefaultRoot: "/",
	NoLinkURL:   false,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &HalalCloudOpen{}
	})
}
