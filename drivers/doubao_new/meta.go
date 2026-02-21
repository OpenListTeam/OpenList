package doubao_new

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	// Usually one of two
	driver.RootID
	// define other
	Authorization string `json:"authorization" help:"DPoP access token (Authorization header value); optional if present in cookie"`
	Dpop          string `json:"dpop" help:"DPoP header value; optional if present in cookie"`
	Cookie        string `json:"cookie" help:"Optional cookie; only used to extract authorization/dpop tokens"`
}

var config = driver.Config{
	Name:              "DoubaoNew",
	LocalSort:         true,
	OnlyProxy:         false,
	NoCache:           false,
	NoUpload:          false,
	NeedMs:            false,
	DefaultRoot:       "",
	CheckStatus:       false,
	Alert:             "",
	NoOverwriteUpload: false,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &DoubaoNew{}
	})
}
