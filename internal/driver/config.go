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
	// disable singleflight for Link generation.
	// if the driver returns Link with MFile, this should be set to trueks
	NoLinkSF bool `json:"only_local"`
}

func (c Config) MustProxy() bool {
	return c.OnlyProxy || c.NoLinkURL
}
