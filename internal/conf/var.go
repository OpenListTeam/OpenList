package conf

import (
	"net"
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
var AuthLoginIPNets      []*net.IPNet
var AuthLoginIPBlackNets []*net.IPNet

var (
	// 在HybridCache中使用[]byte缓存数据流的限制，内存为Go自动管理，直到GC
	AutoMemoryLimit uint64 = 4 * 1024 * 1024
	// 最小空闲内存，当内存不足时，HybridCache会回退到文件缓存。
	// 如果为0，HybridCache会使用文件缓存，不占用内存。
	MinFreeMemory uint64 = 16 * 1024 * 1024
	// 限制HybridCache手动管理内存单次的扩容大小，超过该阈值将分多次扩容。
	// MinFreeMemory大于0时，也限制 Downloader 的PartSize
	MaxBlockLimit uint64 = 16 * 1024 * 1024
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

// IsIPWhitelisted checks if the given IP is within any of the configured whitelist CIDR ranges.
func IsIPWhitelisted(ipStr string) bool {
	return isIPInNets(ipStr, AuthLoginIPNets)
}

// IsIPBlacklisted checks if the given IP is within any of the configured blacklist CIDR ranges.
func IsIPBlacklisted(ipStr string) bool {
	return isIPInNets(ipStr, AuthLoginIPBlackNets)
}

func isIPInNets(ipStr string, nets []*net.IPNet) bool {
	if len(nets) == 0 {
		return false
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, ipNet := range nets {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
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
