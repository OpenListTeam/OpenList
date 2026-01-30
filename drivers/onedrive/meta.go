package onedrive

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Region             string `json:"region" type:"select" required:"true" options:"global,cn,us,de" default:"global"`
	IsSharepoint       bool   `json:"is_sharepoint"`
	UseOnlineAPI       bool   `json:"use_online_api" default:"true"`
	APIAddress         string `json:"api_url_address" default:"https://api.oplist.org/onedrive/renewapi"`
	ClientID           string `json:"client_id"`
	ClientSecret       string `json:"client_secret"`
	RedirectUri        string `json:"redirect_uri" required:"true" default:"https://api.oplist.org/onedrive/callback"`
	RefreshToken       string `json:"refresh_token" required:"true"`
	SiteId             string `json:"site_id"`
	ChunkSize          int64  `json:"chunk_size" type:"number" default:"5"`
	CustomHost         string `json:"custom_host" help:"Custom host for onedrive download link"`
	DisableDiskUsage   bool   `json:"disable_disk_usage" default:"false"`
	EnableDirectUpload bool   `json:"enable_direct_upload" default:"false" help:"Enable direct upload from client to OneDrive"`
	CreateShareLink    bool   `json:"create_share_link" default:"false" help:"Create share link for file download"`
	LinkExpireSeconds  int64  `json:"link_expire_seconds" type:"number" default:"0" help:"Link cache duration in seconds, 0 means no cache"`
}

var config = driver.Config{
	Name:        "Onedrive",
	LocalSort:   true,
	DefaultRoot: "/",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Onedrive{}
	})
}
