package strm

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

const (
	SaveLocalInsertMode = "insert"
	SaveLocalUpdateMode = "update"
	SaveLocalSyncMode   = "sync"
)

type Addition struct {
	Paths             string `json:"paths" required:"true" type:"text"`
	SiteUrl           string `json:"siteUrl" type:"text" required:"false" help:"The prefix URL of the strm file"`
	PathPrefix        string `json:"PathPrefix" type:"text" required:"false" default:"/d"  help:"Path prefix"`
	DownloadFileTypes string `json:"downloadFileTypes" type:"text" default:"ass,srt,vtt,sub,strm" required:"false" help:"Files need to download with strm (usally subtitles)"`
	FilterFileTypes   string `json:"filterFileTypes" type:"text" default:"mp4,mkv,flv,avi,wmv,ts,rmvb,webm,mp3,flac,aac,wav,ogg,m4a,wma,alac" required:"false" help:"Supports suffix name of strm file"`
	EncodePath        bool   `json:"encodePath" default:"true" required:"true" help:"encode the path in the strm file"`
	WithoutUrl        bool   `json:"withoutUrl" default:"false" help:"strm file content without URL prefix"`
	WithSign          bool   `json:"withSign" default:"false"`
	SaveStrmToLocal   bool   `json:"SaveStrmToLocal" default:"false" help:"save strm file locally"`
	SaveStrmLocalPath string `json:"SaveStrmLocalPath" type:"text" help:"save strm file local path"`
	SaveLocalMode     string `json:"SaveLocalMode" type:"select" help:"save strm file locally mode" options:"insert,update,sync" default:"insert"`
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
