package openlist_share

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	driver.RootPath
	Address           string `json:"url" required:"true"`
	ShareId           string `json:"sid" required:"true"`
	Pwd               string `json:"pwd"`
	ForwardArchiveReq bool   `json:"forward_archive_requests" default:"true"`
}

var config = driver.Config{
	Name:        "OpenListShare",
	LocalSort:   true,
	NoUpload:    true,
	DefaultRoot: "/",
}

func New() driver.Driver {
	return &OpenListShare{}
}
