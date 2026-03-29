package onedrive_app

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Region             string `json:"region" type:"select" required:"true" options:"global,cn,us,de" default:"global"`
	ClientID           string `json:"client_id" required:"true"`
	ClientSecret       string `json:"client_secret" required:"true"`
	TenantID           string `json:"tenant_id"`
	Email              string `json:"email"`
	ChunkSize          int64  `json:"chunk_size" type:"number" default:"5"`
	CustomHost         string `json:"custom_host" help:"Custom host for onedrive download link"`
	DisableDiskUsage   bool   `json:"disable_disk_usage" default:"false"`
	EnableDirectUpload bool   `json:"enable_direct_upload" default:"false" help:"Enable direct upload from client to OneDrive"`
}

var config = driver.Config{
	Name:        "OnedriveAPP",
	LocalSort:   true,
	DefaultRoot: "/",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &OnedriveAPP{}
	})
}
