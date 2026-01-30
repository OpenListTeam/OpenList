package teldrive

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Address           string `json:"url" required:"true"`
	Cookie            string `json:"cookie" type:"string" required:"true" help:"access_token=xxx"`
	UseShareLink      bool   `json:"use_share_link" type:"bool" default:"false" help:"Create share link when getting link to support 302. If disabled, you need to enable web proxy."`
	ChunkSize         int64  `json:"chunk_size" type:"number" default:"10" help:"Chunk size in MiB"`
	RandomChunkName   bool   `json:"random_chunk_name" type:"bool" default:"true" help:"Random chunk name"`
	UploadConcurrency int64  `json:"upload_concurrency" type:"number" default:"4" help:"Concurrency upload requests"`
}

var config = driver.Config{
	Name:        "Teldrive",
	DefaultRoot: "/",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Teldrive{}
	})
}
