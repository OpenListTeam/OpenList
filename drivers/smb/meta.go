package smb

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
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

func New() driver.Driver {
	return &SMB{}
}
