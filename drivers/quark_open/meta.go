package quark_open

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	driver.RootID
	OrderBy        string `json:"order_by" type:"select" options:"none,file_type,file_name,updated_at,created_at" default:"none"`
	OrderDirection string `json:"order_direction" type:"select" options:"asc,desc" default:"asc"`
	UseOnlineAPI   bool   `json:"use_online_api" default:"true"`
	APIAddress     string `json:"api_url_address" default:"https://api.oplist.org/quarkyun/renewapi"`
	AccessToken    string `json:"access_token" required:"false" default:""`
	RefreshToken   string `json:"refresh_token" required:"true"`
	AppID          string `json:"app_id" required:"true" help:"Keep it empty if you don't have one"`
	SignKey        string `json:"sign_key" required:"true" help:"Keep it empty if you don't have one"`
}

type Conf struct {
	ua     string
	api    string
	userId string
}

func New() driver.Driver {
	return &QuarkOpen{
		config: driver.Config{
			Name:              "QuarkOpen",
			OnlyProxy:         true,
			DefaultRoot:       "0",
			NoOverwriteUpload: true,
		},
		conf: Conf{
			ua:  "go-resty/3.0.0-beta.1 (https://resty.dev)",
			api: "https://open-api-drive.quark.cn",
		},
	}
}
