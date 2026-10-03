package aliyundrive_share

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	RefreshToken string `json:"refresh_token" required:"true"`
	ShareId      string `json:"share_id" required:"true"`
	SharePwd     string `json:"share_pwd"`
	driver.RootID
	OrderBy        string `json:"order_by" type:"select" options:"name,size,updated_at,created_at"`
	OrderDirection string `json:"order_direction" type:"select" options:"ASC,DESC"`
}

var config = driver.Config{
	Name:        "AliyundriveShare",
	NoUpload:    true,
	DefaultRoot: "root",
}

func New() driver.Driver {
	return &AliyundriveShare{}
}
