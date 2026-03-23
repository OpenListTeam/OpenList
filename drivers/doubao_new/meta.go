package doubao_new

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	// Usually one of two
	driver.RootID
	// define other
	Cookie         string `json:"cookie" required:"true" help:"Web Cookie"`
	AppID          string `json:"app_id" required:"true" default:"497858" help:"Doubao App ID"`
	DPoPKeySecret  string `json:"dpop_key_secret" help:"DPoP Key Secret for generating DPoP token"`
	AuthClientID   string `json:"auth_client_id" help:"Doubao Biz Auth Client ID"`
	AuthClientType string `json:"auth_client_type" help:"Doubao Biz Auth Client Type"`
	AuthScope      string `json:"auth_scope" help:"Doubao Biz Auth Scope"`
	AuthSDKSource  string `json:"auth_sdk_source" help:"Doubao Biz Auth SDK Source"`
	AuthSDKVersion string `json:"auth_sdk_version" help:"Doubao Biz Auth SDK Version"`
	ShareLink      bool   `json:"share_link" help:"Whether to use share link for download"`
	IgnoreJWTCheck bool   `json:"ignore_jwt_check" help:"Whether to ignore JWT check to prevent time issue"`
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
