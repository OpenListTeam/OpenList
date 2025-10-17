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
	// 单个Buffer最大限制
	MaxBufferLimit = 16 * 1024 * 1024
	// 超过该阈值的Buffer将使用 mmap 分配，可主动释放内存
	MmapThreshold = 4 * 1024 * 1024
)
var (
	RawIndexHtml string
	ManageHtml   string
	IndexHtml    string
)
var storagesLoadSignal chan struct{} = make(chan struct{}) // 存储加载完成信号
func StoragesLoadSignal() <-chan struct{} {
	return storagesLoadSignal
}
func SendStoragesLoadSignal() {
	close(storagesLoadSignal)
}
func ResetStoragesLoadSignal() {
	select {
	case <-storagesLoadSignal:
		storagesLoadSignal = make(chan struct{})
	default:
		return
	}
}
func StoragesLoaded() bool {
	select {
	case <-storagesLoadSignal:
		return true
	default:
		return false
	}
}
