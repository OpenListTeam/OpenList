package _189_tv

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	AccessToken    string `json:"access_token"`
	OrderBy        string `json:"order_by" type:"select" options:"filename,filesize,lastOpTime" default:"filename"`
	OrderDirection string `json:"order_direction" type:"select" options:"asc,desc" default:"asc"`
	Type           string `json:"type" type:"select" options:"personal,family" default:"personal"`
	FamilyID       string `json:"family_id"`
	UploadThread   string `json:"upload_thread" default:"3" help:"1<=thread<=32"`
	RapidUpload    bool   `json:"rapid_upload"`
}

var config = driver.Config{
	Name:        "189CloudTV",
	DefaultRoot: "-11",
	CheckStatus: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Cloud189TV{}
	})
}
