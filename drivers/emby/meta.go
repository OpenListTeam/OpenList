package emby

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	URL        string `json:"url" required:"true"`
	ApiKey     string `json:"api_key"`
	UserID     string `json:"user_id"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	LinkMethod string `json:"link_method" type:"select" options:"stream,download" default:"stream"`
}

var config = driver.Config{
	Name:        "Emby",
	LocalSort:   true,
	NoUpload:    true,
	DefaultRoot: "1",
	CheckStatus: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Emby{}
	})
}
