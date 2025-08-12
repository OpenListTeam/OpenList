package cloudreve

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	// Usually one of two
	driver.RootPath
	// define other
	Address                  string `json:"address" required:"true"`
	Username                 string `json:"username"`
	Password                 string `json:"password"`
	Cookie                   string `json:"cookie"`
	CustomUA                 string `json:"custom_ua"`
	EnableThumbAndFolderSize bool   `json:"enable_thumb_and_folder_size"`
}

var config = driver.Config{
	Name:        "Cloudreve",
	DefaultRoot: "/",
	LocalSort:   true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Cloudreve{}
	})
}
