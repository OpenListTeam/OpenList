package alias

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	Paths                string `json:"paths" required:"true" type:"text"`
	ReadConflictPolicy   string `json:"read_conflict_policy" type:"select" options:"first,random,all" default:"first"`
	WriteConflictPolicy  string `json:"write_conflict_policy" type:"select" options:"disabled,first,deterministic,deterministic_or_all,all,all_strict" default:"disabled" help:"How the driver handles identical backend paths when renaming, removing, or making directories."`
	PutConflictPolicy    string `json:"put_conflict_policy" type:"select" options:"disabled,first,deterministic,deterministic_or_all,all,all_strict,random,quota,quota_strict" default:"disabled" help:"How the driver handles identical backend paths when uploading, copying, moving, or decompressing."`
	FileConsistencyCheck bool   `json:"file_consistency_check" type:"bool" default:"false"`
	DownloadConcurrency  int    `json:"download_concurrency" default:"0" required:"false" type:"number" help:"Need to enable proxy"`
	DownloadPartSize     int    `json:"download_part_size" default:"0" type:"number" required:"false" help:"Need to enable proxy. Unit: KB"`
	ProviderPassThrough  bool   `json:"provider_pass_through" type:"bool" default:"false"`
	DetailsPassThrough   bool   `json:"details_pass_through" type:"bool" default:"false"`
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
		return &Alias{}
	})
}
