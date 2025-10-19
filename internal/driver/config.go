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
	// LinkCacheType
	//  =-1 use LinkCacheTypeGetter interface get the value
	//  =0  no extra info added to cache key (default)
	//  =1  add IP to cache key
	//  =2  add UserAgent to cache key
	//  =3  add IP and UserAgent to cache key
	LinkCacheType int8 `json:"-"`
}

func (c Config) MustProxy() bool {
	return c.OnlyProxy || c.NoLinkURL
}
