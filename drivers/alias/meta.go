package alias

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	// Usually one of two
	// driver.RootPath
	// define other
	Paths               string `json:"paths" required:"true" type:"text"`
	ProtectSameName     bool   `json:"protect_same_name" default:"true" required:"false" help:"Protects same-name files from Delete or Rename"`
	ParallelWrite       bool   `json:"parallel_write" type:"bool" default:"false"`
	DownloadConcurrency int    `json:"download_concurrency" default:"0" required:"false" type:"number" help:"Need to enable proxy"`
	DownloadPartSize    int    `json:"download_part_size" default:"0" type:"number" required:"false" help:"Need to enable proxy. Unit: KB"`
	Writable            bool   `json:"writable" type:"bool" default:"false"`
	ProviderPassThrough bool   `json:"provider_pass_through" type:"bool" default:"false"`
	DetailsPassThrough  bool   `json:"details_pass_through" type:"bool" default:"false"`
}

var config = driver.Config{
	Name:             "Alias",
	LocalSort:        true,
	NoCache:          true,
	NoUpload:         false,
	DefaultRoot:      "/",
	ProxyRangeOption: true,
	LinkCacheMode:    driver.LinkCacheAuto,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Alias{
			Addition: Addition{
				ProtectSameName: true,
			},
		}
	})
}
