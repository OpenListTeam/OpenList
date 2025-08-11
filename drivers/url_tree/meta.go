package url_tree

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	// Usually one of two
	// driver.RootPath
	// driver.RootID
	// define other
	UrlStructure string `json:"url_structure" type:"text" required:"true" default:"https://raw.githubusercontent.com/OpenListTeam/OpenList/main/README.md\nhttps://raw.githubusercontent.com/OpenListTeam/OpenList/main/README_cn.md\nfolder:\n  CONTRIBUTING.md:1635:https://raw.githubusercontent.com/OpenListTeam/OpenList/main/CONTRIBUTING.md\n  CODE_OF_CONDUCT.md:2093:https://raw.githubusercontent.com/OpenListTeam/OpenList/main/CODE_OF_CONDUCT.md" help:"structure:FolderName:\n  [FileName:][FileSize:][Modified:]Url"`
	HeadSize     bool   `json:"head_size" type:"bool" default:"false" help:"Use head method to get file size, but it may be failed."`
	Writable     bool   `json:"writable" type:"bool" default:"false"`
}

var config = driver.Config{
	Name:        "UrlTree",
	LocalSort:   true,
	NoCache:     true,
	CheckStatus: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Urls{}
	})
}
