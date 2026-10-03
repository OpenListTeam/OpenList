package weiyun_open

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	MCPToken         string `json:"mcp_token" required:"true"`
	EnvID            string `json:"env_id"`
	APIURL           string `json:"api_url" default:"https://www.weiyun.com/api/v3/mcpserver"`
	RootDirKey       string `json:"root_dir_key"`
	RootPDirKey      string `json:"root_pdir_key"`
	OrderBy          string `json:"order_by" type:"select" options:"name,modified,none" default:"name"`
	OrderDirection   string `json:"order_direction" type:"select" options:"asc,desc" default:"asc"`
	DeleteCompletely bool   `json:"delete_completely" type:"bool" default:"false"`
}

var config = driver.Config{
	Name:        "WeiYun Open",
	OnlyProxy:   true,
	CheckStatus: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &WeiYunOpen{}
	})
}
