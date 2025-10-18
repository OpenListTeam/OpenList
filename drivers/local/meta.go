package local

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	DirectorySize    bool   `json:"directory_size" default:"false" help:"This might impact host performance"`
	Thumbnail        bool   `json:"thumbnail" required:"true" help:"enable thumbnail"`
	ThumbCacheFolder string `json:"thumb_cache_folder"`
	ThumbConcurrency string `json:"thumb_concurrency" default:"16" required:"false" help:"Number of concurrent thumbnail generation goroutines. This controls how many thumbnails can be generated in parallel."`
	VideoThumbPos    string `json:"video_thumb_pos" default:"20%" required:"false" help:"The position of the video thumbnail. If the value is a number (integer ot floating point), it represents the time in seconds. If the value ends with '%', it represents the percentage of the video duration."`
	ShowHidden       bool   `json:"show_hidden" default:"true" required:"false" help:"show hidden directories and files"`
	MkdirPerm        string `json:"mkdir_perm" default:"777"`
	RecycleBinPath   string `json:"recycle_bin_path" default:"delete permanently" help:"path to recycle bin, delete permanently if empty or keep 'delete permanently'"`
}

var config = driver.Config{
	Name:        "Local",
	LocalSort:   true,
	OnlyProxy:   true,
	NoCache:     true,
	DefaultRoot: "/",
	NoLinkURL:   true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Local{
			directoryMap: DirectoryMap{},
		}
	})
}
