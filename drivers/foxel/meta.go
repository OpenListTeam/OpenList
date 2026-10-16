package foxel

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Address    string `json:"url" required:"true" help:"Foxel server URL, without /api"`
	Username   string `json:"username" help:"Use username and password for automatic reauthentication"`
	Password   string `json:"password"`
	Token      string `json:"token" help:"Bearer access token; optional when username and password are set"`
	PageSize   int    `json:"page_size" type:"number" default:"200" help:"Directory page size (1-500)"`
	LinkExpire int    `json:"link_expire" type:"number" default:"3600" help:"Temporary download link lifetime in seconds"`
}

var config = driver.Config{
	Name:             "Foxel",
	LocalSort:        true,
	DefaultRoot:      "/",
	ProxyRangeOption: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Foxel{}
	})
}
