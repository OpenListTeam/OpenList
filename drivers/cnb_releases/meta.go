package cnb_releases

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	driver.RootID
	Repo          string `json:"repo" type:"string" required:"true"`
	Token         string `json:"token" type:"string" required:"true"`
	UseTagName    bool   `json:"use_tag_name" type:"bool" default:"false" help:"Use tag name instead of release name"`
	DefaultBranch string `json:"default_branch" type:"string" default:"main" help:"Default branch for new releases"`
}

var config = driver.Config{
	Name:      "CNB Releases",
	LocalSort: true,
}

func New() driver.Driver {
	return &CnbReleases{}
}
