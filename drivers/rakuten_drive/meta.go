package rakuten_drive

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	HostID         string `json:"host_id" required:"true" help:"host_id from Rakuten Drive API"`
	RefreshToken   string `json:"refresh_token" required:"true" help:"refresh_token from auth/refreshtoken"`
	UploadToken    string `json:"upload_token" required:"false" help:"token header for upload endpoints (if required)"`
	AppVersion     string `json:"app_version" required:"false" default:"v21.11.10"`
	PageSize       int    `json:"page_size" type:"number" default:"200" help:"list page size"`
	UploadPartSize int64  `json:"upload_part_size" type:"number" default:"5242880" help:"S3 multipart part size in bytes (min 5MB)"`
}

var config = driver.Config{
	Name:        "RakutenDrive",
	DefaultRoot: "/",
	LocalSort:   false,
	PreferProxy: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &RakutenDrive{
			Addition: Addition{
				AppVersion:     "v21.11.10",
				PageSize:       200,
				UploadPartSize: 5 * 1024 * 1024,
			},
		}
	})
}
