package conf

import (
	"net/url"
	"regexp"
	"sync"
)

var (
	BuiltAt    string = "unknown"
	GitAuthor  string = "unknown"
	GitCommit  string = "unknown"
	Version    string = "dev"
	WebVersion string = "rolling"
)

var (
	Conf       *Config
	URL        *url.URL
	ConfigPath string
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

var (
	// StoragesLoaded loaded success if empty
	StoragesLoaded     = false
	storagesLoadMu     sync.RWMutex
	storagesLoadSignal chan struct{} = make(chan struct{})
)

func StoragesLoadSignal() <-chan struct{} {
	storagesLoadMu.RLock()
	ch := storagesLoadSignal
	storagesLoadMu.RUnlock()
	return ch
}
func SendStoragesLoadedSignal() {
	storagesLoadMu.Lock()
	select {
	case <-storagesLoadSignal:
		// already closed
	default:
		StoragesLoaded = true
		close(storagesLoadSignal)
	}
	storagesLoadMu.Unlock()
}
func ResetStoragesLoadSignal() {
	storagesLoadMu.Lock()
	select {
	case <-storagesLoadSignal:
		StoragesLoaded = false
		storagesLoadSignal = make(chan struct{})
	default:
		// not closed -> nothing to do
	}
	storagesLoadMu.Unlock()
}
