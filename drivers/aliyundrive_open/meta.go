package aliyundrive_open

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	DriveType string `json:"drive_type" type:"select" options:"default,resource,backup" default:"resource"`
	driver.RootID
	RefreshToken       string `json:"refresh_token" required:"true"`
	OrderBy            string `json:"order_by" type:"select" options:"name,size,updated_at,created_at"`
	OrderDirection     string `json:"order_direction" type:"select" options:"ASC,DESC"`
	UseOnlineAPI       bool   `json:"use_online_api" default:"true"`
	AlipanType         string `json:"alipan_type" required:"true" type:"select" default:"default" options:"default,alipanTV"`
	APIAddress         string `json:"api_url_address" default:"https://api.oplist.org/alicloud/renewapi"`
	ClientID           string `json:"client_id" help:"Keep it empty if you don't have one"`
	ClientSecret       string `json:"client_secret" help:"Keep it empty if you don't have one"`
	RemoveWay          string `json:"remove_way" required:"true" type:"select" options:"trash,delete"`
	RapidUpload        bool   `json:"rapid_upload" help:"If you enable this option, the file will be uploaded to the server first, so the progress will be incorrect"`
	InternalUpload     bool   `json:"internal_upload" help:"If you are using Aliyun ECS is located in Beijing, you can turn it on to boost the upload speed"`
	LIVPDownloadFormat string `json:"livp_download_format" type:"select" options:"jpeg,mov" default:"jpeg"`
	AccessToken        string
	RootPath           string
}

var config = driver.Config{
	Name:              "AliyundriveOpen",
	DefaultRoot:       "root",
	NoOverwriteUpload: true,
}
var API_URL = "https://openapi.alipan.com"

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &AliyundriveOpen{}
	})
}
