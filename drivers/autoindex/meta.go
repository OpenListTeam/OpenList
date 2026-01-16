package autoindex

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	URL                string `json:"url" required:"true"`
	ItemXPath          string `json:"item_xpath" required:"true"`
	NameXPath          string `json:"name_xpath" required:"true"`
	ModifiedXPath      string `json:"modified_xpath"`
	SizeXPath          string `json:"size_xpath"`
	IgnoreFileNames    string `json:"ignore_file_names" type:"text" default:".\n..\nParent Directory\nUp"`
	ModifiedTimeFormat string `json:"modified_time_format" default:"02-Jan-2006 15:04" help:"Must be based on the time point Mon Jan 2 15:04:05 -0700 MST 2006"`
}

var config = driver.Config{
	Name:        "AutoIndex",
	LocalSort:   true,
	CheckStatus: true,
	NoUpload:    true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &AutoIndex{}
	})
}
