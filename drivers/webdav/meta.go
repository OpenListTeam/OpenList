package webdav

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	Vendor   string `json:"vendor" type:"select" options:"sharepoint,other" default:"other"`
	Address  string `json:"address" required:"true"`
	Username string `json:"username" required:"true"`
	Password string `json:"password" required:"true"`
	driver.RootPath
	TlsInsecureSkipVerify bool `json:"tls_insecure_skip_verify" default:"false"`
	Enable302             bool `json:"enable_302" default:"false" help:"Return redirected URL instead of proxying"`
}

var config = driver.Config{
	Name:        "WebDav",
	LocalSort:   true,
	OnlyProxy:   true,
	DefaultRoot: "/",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &WebDav{}
	})
}
