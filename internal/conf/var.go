package conf

import (
	"net/url"
	"regexp"
)

var (
	BuiltAt    string = "unknown"
	GitAuthor  string = "unknown"
	GitCommit  string = "unknown"
	Version    string = "dev"
	WebVersion string = "rolling"
)

var (
	Conf *Config
	URL  *url.URL
)

var SlicesMap = make(map[string][]string)
var FilenameCharMap = make(map[string]string)
var PrivacyReg []*regexp.Regexp

var (
	// StoragesLoaded loaded success if empty
	StoragesLoaded = false
	// 单个Buffer最大限制
	MaxBufferLimit = 0
	// 超过该阈值的Buffer将使用 mmap 分配，可主动释放内存
	MinMmapAllocSize = 0
)
var (
	RawIndexHtml string
	ManageHtml   string
	IndexHtml    string
)
