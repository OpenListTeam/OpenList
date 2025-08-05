package cnb_releases

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Repo  string `json:"repo" type:"string" required:"false"`
	Token string `json:"token" type:"string" required:"false"`
}

var config = driver.Config{
	Name:              "CNB Releases",
	LocalSort:         true,
	DefaultRoot:       "",
	NoUpload:          true,
	NoOverwriteUpload: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &CnbReleases{}
	})
}
