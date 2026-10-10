package ipfs

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	// Usually one of two
	driver.RootPath
	Mode     string `json:"mode" options:"ipfs,ipns,mfs" type:"select" required:"true"`
	Endpoint string `json:"endpoint" default:"http://127.0.0.1:5001" required:"true"`
	Gateway  string `json:"gateway" default:"http://127.0.0.1:8080" required:"true"`
}

var config = driver.Config{
	Name:        "IPFS API",
	DefaultRoot: "/",
	LocalSort:   true,
}

func New() driver.Driver {
	return &IPFS{}
}
