package template

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	// Usually one of two
	driver.RootID
	// define other
	RefreshToken string `json:"refresh_token" required:"true"`
	FamilyID     string `json:"family_id" help:"Keep it empty if you want to use your personal drive"`
	SortRule     string `json:"sort_rule" type:"select" options:"name_asc,name_desc,time_asc,time_desc,size_asc,size_desc" default:"name_asc"`

	AccessToken string `json:"access_token"`
}

var config = driver.Config{
	Name:              "WoPan",
	DefaultRoot:       "0",
	NoOverwriteUpload: true,
}

func New() driver.Driver {
	return &Wopan{}
}
