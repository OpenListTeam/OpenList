package bunny_storage

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	StorageZoneName   string `json:"storage_zone_name" required:"true"`
	AccessKey         string `json:"access_key" required:"true"`
	Endpoint          string `json:"endpoint" required:"true" default:"storage.bunnycdn.com"`
	CDNBaseURL        string `json:"cdn_base_url"`
	CDNTokenKey       string `json:"cdn_token_key"`
	CDNTokenMethod    string `json:"cdn_token_method" type:"select" options:"sha256,hmac_sha256" default:"sha256"`
	CDNTokenIncludeIP bool   `json:"cdn_token_include_ip" default:"false"`
	SignURLExpire     int    `json:"sign_url_expire" type:"number" default:"4"`
	Placeholder       string `json:"placeholder" default:".openlist"`
}

var config = driver.Config{
	Name:        "Bunny Storage",
	LocalSort:   true,
	DefaultRoot: "/",
	CheckStatus: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &BunnyStorage{}
	})
}
