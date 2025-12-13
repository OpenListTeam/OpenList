package openlist

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Address           string `json:"url" required:"true"`
	MetaPassword      string `json:"meta_password"`
	Username          string `json:"username"`
	Password          string `json:"password"`
	Token             string `json:"token"`
	PassIPToUpsteam   bool   `json:"pass_ip_to_upsteam" default:"true"`
	PassUAToUpsteam   bool   `json:"pass_ua_to_upsteam" default:"true"`
	ForwardArchiveReq bool   `json:"forward_archive_requests" default:"true"`
}

var config = driver.Config{
	Name:             "OpenList",
	LocalSort:        true,
	DefaultRoot:      "/",
	ProxyRangeOption: true,
	LinkCacheMode:    driver.LinkCacheAuto,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &OpenList{}
	})
}
