package chunk

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	RemotePath string `json:"remote_path" required:"true"`
	PartSize   int64  `json:"part_size" required:"true" type:"number" help:"bytes"`
	CustomExt  string `json:"custom_ext" type:"string"`
}

var config = driver.Config{
	Name:        "Chunk",
	LocalSort:   true,
	OnlyProxy:   true,
	NoCache:     true,
	DefaultRoot: "/",
	NoLinkURL:   true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Chunk{}
	})
}
