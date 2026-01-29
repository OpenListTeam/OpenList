package driver

type Config struct {
	Name      string `json:"name"`
	LocalSort bool   `json:"local_sort"`
	OnlyProxy bool   `json:"only_proxy"`
	NoCache   bool   `json:"no_cache"`
	NoUpload  bool   `json:"no_upload"`
	// if need get message from user, such as validate code
	NeedMs      bool   `json:"need_ms"`
	DefaultRoot string `json:"default_root"`
	CheckStatus bool   `json:"-"`
	//info,success,warning,danger
	Alert string `json:"alert"`
	// whether to support overwrite upload
	NoOverwriteUpload bool `json:"-"`
	ProxyRangeOption  bool `json:"-"`
	// if the driver returns Link without URL, this should be set to true
	NoLinkURL bool `json:"-"`
	// Link cache behaviour:
	//  - LinkCacheAuto: let driver decide per-path (implement driver.LinkCacheModeResolver)
	//  - LinkCacheNone: no extra info added to cache key (default)
	//  - flags (OR-able) can add more attributes to cache key (IP, UA, ...)
	LinkCacheMode `json:"-"`
	// if the driver only store indices of files (e.g. UrlTree)
	OnlyIndices bool `json:"only_indices"`
	// prefer proxy download even if direct link is available
	PreferProxy bool `json:"prefer_proxy"`
}
type LinkCacheMode int8

const (
	LinkCacheAuto LinkCacheMode = -1 // Let the driver decide per-path (use driver.LinkCacheModeResolver)
	LinkCacheNone LinkCacheMode = 0  // No extra info added to cache key (default)
)

const (
	LinkCacheIP LinkCacheMode = 1 << iota // include client IP in cache key
	LinkCacheUA                           // include User-Agent in cache key
)

func (c Config) MustProxy() bool {
	return c.OnlyProxy || c.NoLinkURL
}

func (c Config) DefaultProxy() bool {
	return c.PreferProxy
}
