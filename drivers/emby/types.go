package emby

type authReq struct {
	Username string `json:"Username"`
	Pw       string `json:"Pw"`
}

type authResp struct {
	AccessToken string `json:"AccessToken"`
	User        struct {
		ID string `json:"Id"`
	} `json:"User"`
}

type listResp struct {
	Items            []embyItem `json:"Items"`
	TotalRecordCount int        `json:"TotalRecordCount"`
}

type embyItem struct {
	Name        string `json:"Name"`
	ID          string `json:"Id"`
	Type        string `json:"Type"`
	Path        string `json:"Path"`
	SeriesName  string `json:"SeriesName"`
	IndexNumber int    `json:"IndexNumber"`
	ParentIndex int    `json:"ParentIndexNumber"`
	IsFolder    bool   `json:"IsFolder"`
	Size        int64  `json:"Size"`
	DateCreated string `json:"DateCreated"`
}

type itemDetailResp struct {
	MediaSources []embyMediaSource `json:"MediaSources"`
}

type embyMediaSource struct {
	ID                   string `json:"Id"`
	Container            string `json:"Container"`
	SupportsDirectStream bool   `json:"SupportsDirectStream"`
}
