package sftp

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	Address    string `json:"address" required:"true"`
	Username   string `json:"username" required:"true"`
	PrivateKey string `json:"private_key" type:"text"`
	Password   string `json:"password"`
	Passphrase string `json:"passphrase"`
	driver.RootPath
	IgnoreSymlinkError bool `json:"ignore_symlink_error" default:"false" info:"Ignore symlink error"`
}

var config = driver.Config{
	Name:        "SFTP",
	LocalSort:   true,
	OnlyProxy:   true,
	DefaultRoot: "/",
	CheckStatus: true,
	NoLinkURL:   true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &SFTP{}
	})
}
