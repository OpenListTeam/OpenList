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
	// 限制单次内存的扩容大小，超过该阈值将分多次扩容。
	// CacheThreshold大于0时，也限制 Downloader 的PartSize
	MaxBlockLimit uint64 = 16 * 1024 * 1024
	// 大于该阈值的数据流将使用HybridCache，可主动释放内存。
	// 否则使用Go的内存分配，直到GC回收。
	CacheThreshold uint64 = 4 * 1024 * 1024
	// 最小空闲内存，当内存不足时，HybridCache会回退到文件缓存。
	// 如果为0，HybridCache会使用文件缓存，不占用内存。
	MinFreeMemory uint64 = 16 * 1024 * 1024
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
