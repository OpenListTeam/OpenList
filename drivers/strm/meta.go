package strm

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	Paths             string `json:"paths" required:"true" type:"text"`
	SiteUrl           string `json:"siteUrl" type:"text" required:"false" help:"The prefix URL of the strm file"`
	DownloadFileTypes string `json:"downloadFileTypes" type:"text" default:"ass,srt,vtt,sub,strm" required:"false" help:"Files need to download with strm (usally subtitles)"`
	FilterFileTypes   string `json:"filterFileTypes" type:"text" default:"mp4,mkv,flv,avi,wmv,ts,rmvb,webm,mp3,flac,aac,wav,ogg,m4a,wma,alac" required:"false" help:"Supports suffix name of strm file"`
	EncodePath        bool   `json:"encodePath" default:"true" required:"true" help:"encode the path in the strm file"`
	WithoutUrl        bool   `json:"withoutUrl" default:"false" help:"strm file content without URL prefix"`
	SaveStrmToLocal   bool   `json:"SaveStrmToLocal" default:"false" help:"save strm file locally"`
	SaveStrmLocalPath string `json:"SaveStrmLocalPath" type:"text" help:"save strm file local path"`
	Version           int
}

var config = driver.Config{
	Name:        "Strm",
	LocalSort:   true,
	OnlyProxy:   true,
	NoCache:     true,
	NoUpload:    true,
	DefaultRoot: "/",
	NoLinkURL:   true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Strm{
			Addition: Addition{
				EncodePath: true,
			},
		}
	})
}
