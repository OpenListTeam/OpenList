package feishu

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	AuthMode       string `json:"auth_mode" type:"select" options:"tenant,user" default:"tenant" required:"true" help:"Authentication identity"`
	AppID          string `json:"app_id" required:"true" help:"Required for both authentication modes"`
	AppSecret      string `json:"app_secret" required:"true" help:"Required for both authentication modes"`
	RedirectURI    string `json:"redirect_uri" help:"Required only for user OAuth"`
	ReadOnly       bool   `json:"read_only" default:"true"`
	DocumentFormat string `json:"document_format" type:"select" options:"docx,pdf" default:"docx"`
	RefreshToken   string `json:"refresh_token"`
	AccessToken    string `json:"access_token"`
	TokenExpiresAt int64  `json:"token_expires_at" type:"number"`
}

var config = driver.Config{
	Name: "Feishu", LocalSort: true, OnlyProxy: true, NoLinkURL: true,
	CheckStatus: true,
	NoCache:     true, NoOverwriteUpload: true,
}

func init() { op.RegisterDriver(func() driver.Driver { return &Feishu{} }) }
