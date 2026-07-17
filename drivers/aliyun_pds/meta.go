package aliyun_pds

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	DomainID       string `json:"domain_id" required:"true"`
	DriveID        string `json:"drive_id" required:"true"`
	RefreshToken   string `json:"refresh_token" required:"true"`
	OrderBy        string `json:"order_by" type:"select" options:"name,size,updated_at,created_at"`
	OrderDirection string `json:"order_direction" type:"select" options:"ASC,DESC"`
	RapidUpload    bool   `json:"rapid_upload"`
}

var config = driver.Config{
	Name:              "Aliyun PDS",
	DefaultRoot:       "root",
	NoOverwriteUpload: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &AliPDS{}
	})
}
