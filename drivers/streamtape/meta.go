package streamtape

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	APILogin string `json:"api_login" required:"true" help:"API Login from Streamtape account settings"`
	APIKey   string `json:"api_key" required:"true" help:"API Key from Streamtape account settings"`
}

var config = driver.Config{
	Name:              "Streamtape",
	LocalSort:         false,
	OnlyProxy:         true,
	NoCache:           false,
	NoUpload:          false,
	NeedMs:            false,
	DefaultRoot:       "0",
	CheckStatus:       false,
	Alert:             "warning|Moving files to root folder is not supported by Streamtape API",
	NoOverwriteUpload: false,
	ProxyRangeOption:  true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Streamtape{}
	})
}
