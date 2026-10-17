package _123Link

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	OriginURLs    string `json:"origin_urls" type:"text" required:"true" default:"https://vip.123pan.com/29/folder/file.mp3" help:"structure:FolderName:\n  [FileSize:][Modified:]Url"`
	PrivateKey    string `json:"private_key"`
	UID           uint64 `json:"uid" type:"number"`
	ValidDuration int64  `json:"valid_duration" type:"number" default:"30" help:"minutes"`
}

var config = driver.Config{
	Name: "123PanLink",
}

func New() driver.Driver {
	return &Pan123Link{}
}
