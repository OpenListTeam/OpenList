package template

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	// Usually one of two
	driver.RootID
	// define other
	RefreshToken string `json:"refresh_token" help:"Provide a refresh token or an access token (at least 16 characters)"`
	FamilyID     string `json:"family_id" help:"Keep it empty if you want to use your personal drive"`
	SortRule     string `json:"sort_rule" type:"select" options:"name_asc,name_desc,time_asc,time_desc,size_asc,size_desc" default:"name_asc"`

	AccessToken string `json:"access_token" help:"Optional when a refresh token is provided; it is refreshed automatically when missing or too short"`
}

var config = driver.Config{
	Name:              "WoPan",
	DefaultRoot:       "0",
	NoOverwriteUpload: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Wopan{}
	})
}
