package smb

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Address   string `json:"address" required:"true"`
	Username  string `json:"username" required:"true"`
	Password  string `json:"password"`
	ShareName string `json:"share_name" required:"true"`
}

var config = driver.Config{
	Name:        "SMB",
	LocalSort:   true,
	OnlyProxy:   true,
	DefaultRoot: ".",
	NoCache:     true,
	NoLinkURL:   true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &SMB{}
	})
}
