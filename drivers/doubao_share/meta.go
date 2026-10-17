package doubao_share

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	driver.RootPath
	Cookie   string `json:"cookie" type:"text"`
	ShareIds string `json:"share_ids" type:"text" required:"true"`
}

var config = driver.Config{
	Name:        "DoubaoShare",
	LocalSort:   true,
	NoUpload:    true,
	DefaultRoot: "/",
}

func New() driver.Driver {
	return &DoubaoShare{}
}
