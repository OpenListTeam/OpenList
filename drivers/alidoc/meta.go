package alidoc

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	driver.RootID
	Cookie string `json:"cookie" type:"text" required:"true" help:"钉钉文档网页 Cookie"`
}

var config = driver.Config{
	Name:      "AliDoc",
	LocalSort: true,
}

func New() driver.Driver {
	return &AliDoc{}
}
