package pds

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	DomainID     string `json:"domain_id" required:"true" help:"PDS domain id"`
	DriveID      string `json:"drive_id" required:"true" help:"PDS drive id"`
	ClientID     string `json:"client_id" default:"lMNVp25Sd1MfqZDQ"`
	AccessToken  string `json:"access_token" type:"text" help:"Short-lived PDS access token; either access_token or refresh_token is required"`
	RefreshToken string `json:"refresh_token" type:"text"`
	TokenType    string `json:"token_type" default:"Bearer"`
	ExpiresAt    int64  `json:"expires_at" type:"number" help:"Unix timestamp in seconds; leave 0 if unknown"`
}

var config = driver.Config{
	Name:        "PDS",
	DefaultRoot: "root",
	LocalSort:   false,
	CheckStatus: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &PDS{}
	})
}
