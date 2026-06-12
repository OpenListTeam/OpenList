package tencent_cos

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Bucket              string `json:"bucket" required:"true" help:"Bucket name in format: BucketName-APPID"`
	Region              string `json:"region" required:"true" help:"COS region, e.g. ap-beijing, ap-shanghai"`
	SecretID            string `json:"secret_id" required:"true"`
	SecretKey           string `json:"secret_key" required:"true"`
	CustomHost          string `json:"custom_host" help:"Custom domain for generating download links"`
	SignURLExpire       int    `json:"sign_url_expire" type:"number" default:"4" help:"Presigned URL expiration time in hours"`
	Placeholder         string `json:"placeholder" help:"Placeholder file name for marking directories"`
	EnableDirectUpload  bool   `json:"enable_direct_upload" default:"false"`
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &TencentCOS{
			config: driver.Config{
				Name:        "TencnetCOS",
				DefaultRoot: "/",
				LocalSort:   true,
				CheckStatus: true,
			},
		}
	})
}
