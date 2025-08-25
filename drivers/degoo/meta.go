package degoo

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	Username     string `json:"username" help:"Your Degoo account email"`
	Password     string `json:"password" help:"Your Degoo account password"`
	RefreshToken string `json:"refresh_token" help:"Refresh token for automatic token renewal, obtained automatically"`
	AccessToken  string `json:"access_token" help:"Access token for Degoo API, obtained automatically"`
}

var config = driver.Config{
	Name:              "Degoo",
	LocalSort:         true,
	DefaultRoot:       "0",
	NoOverwriteUpload: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Degoo{}
	})
}
