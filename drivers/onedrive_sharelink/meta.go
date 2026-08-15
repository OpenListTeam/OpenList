package onedrive_sharelink

import (
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	ShareLinkURL       string `json:"url" required:"true"`
	ShareLinkPassword  string `json:"password"`
	DisableDiskUsage   bool   `json:"disable_disk_usage" default:"false"`
	EnableDirectUpload bool   `json:"enable_direct_upload" default:"false" help:"Allow uploading directly to OneDrive without going through OpenList"`
	IsSharepoint       bool
	downloadLinkPrefix string
	Headers            http.Header
	HeaderTime         int64
	DriveURL           string
	DriveAccessToken   string
	DriveTokenTime     int64
	driveRootPath      string
}

var config = driver.Config{
	Name:        "Onedrive Sharelink",
	LocalSort:   true,
	DefaultRoot: "/",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &OnedriveSharelink{}
	})
}
