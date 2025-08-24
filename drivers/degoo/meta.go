package degoo

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	Username string `json:"username" required:"true" help:"Your Degoo account email"`
	Password string `json:"password" required:"true" help:"Your Degoo account password"`
	Token    string `json:"token" help:"Access token for Degoo API, will be obtained automatically if not provided"`
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
