package chunk

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	RemotePath         string `json:"remote_path" required:"true"`
	PartSize           int64  `json:"part_size" required:"true" type:"number" help:"bytes"`
	ChunkLargeFileOnly bool   `json:"chunk_large_file_only" default:"false" help:"chunk only if file size > part_size"`
	ChunkPrefix        string `json:"chunk_prefix" type:"string" default:"[openlist_chunk]" help:"the prefix of chunk folder"`
	CustomExt          string `json:"custom_ext" type:"string"`
	StoreHash          bool   `json:"store_hash" type:"bool" default:"true"`
	NumListWorkers     int    `json:"num_list_workers" required:"true" type:"number" default:"5"`

	Thumbnail  bool `json:"thumbnail" required:"true" default:"false" help:"enable thumbnail which pre-generated under .thumbnails folder"`
	ShowHidden bool `json:"show_hidden"  default:"true" required:"false" help:"show hidden directories and files"`
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
		return &Chunk{
			Addition: Addition{
				ChunkPrefix:    "[openlist_chunk]",
				NumListWorkers: 5,
			},
		}
	})
}
