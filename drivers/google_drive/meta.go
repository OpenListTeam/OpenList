package google_drive

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	RefreshToken     string `json:"refresh_token" required:"true"`
	OrderBy          string `json:"order_by" type:"string" help:"such as: folder,name,modifiedTime"`
	OrderDirection   string `json:"order_direction" type:"select" options:"asc,desc"`
	UseOnlineAPI     bool   `json:"use_online_api" default:"true"`
	APIAddress       string `json:"api_url_address" default:"https://api.oplist.org/googleui/renewapi"`
	ClientID         string `json:"client_id"`
	ClientSecret     string `json:"client_secret"`
	ChunkSize        int64  `json:"chunk_size" type:"number" default:"5" help:"chunk size while uploading (unit: MB)"`
	DisableDiskUsage bool   `json:"disable_disk_usage" default:"false"`
}

var config = driver.Config{
	Name:        "GoogleDrive",
	OnlyProxy:   true,
	DefaultRoot: "root",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &GoogleDrive{}
	})
}
