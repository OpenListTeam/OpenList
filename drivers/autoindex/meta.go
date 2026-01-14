package autoindex

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	URL                string `json:"url" required:"true"`
	ItemXPath          string `json:"item_xpath" required:"true"`
	NameXPath          string `json:"name_xpath" required:"true"`
	ModifiedXPath      string `json:"modified_xpath" required:"true"`
	SizeXPath          string `json:"size_xpath" required:"true"`
	IgnoreFileNames    string `json:"ignore_file_names" type:"text" default:".\n..\nParent Directory"`
	ModifiedTimeFormat string `json:"modified_time_format" default:"2006-01-02 15:04"`
}

var config = driver.Config{
	Name:        "Autoindex",
	LocalSort:   true,
	CheckStatus: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Autoindex{}
	})
}
