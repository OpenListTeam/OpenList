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
	DpopKeyPair   string `json:"dpop_key_pair" help:"DPoP key pair for refreshing Dpop; optional if present in cookie"`
	Cookie        string `json:"cookie" help:"Optional cookie; only used to extract authorization/dpop tokens"`
}

var config = driver.Config{
	Name:        "DoubaoNew",
	LocalSort:   true,
	DefaultRoot: "",
	Alert: `danger|Do not use 302 if the storage is public accessible.
Otherwise, the download link may leak sensitive information such as access token or signature.
Others may use the leaked link to access all your files.`,
	NoOverwriteUpload: false,
	PreferProxy:       true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &DoubaoNew{}
	})
}
