package _189

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	Username string `json:"username" required:"true"`
	Password string `json:"password" required:"true"`
	Cookie   string `json:"cookie" help:"Fill in the cookie if need captcha"`
	driver.RootID
}

var config = driver.Config{
	Name:        "189Cloud",
	LocalSort:   true,
	DefaultRoot: "-11",
	Alert:       `info|You can try to use 189PC driver if this driver does not work.`,
}

func New() driver.Driver {
	return &Cloud189{}
}
