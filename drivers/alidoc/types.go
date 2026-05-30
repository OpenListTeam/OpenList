package alidoc

import "github.com/OpenListTeam/OpenList/v4/internal/model"

type apiResp struct {
	Status    int    `json:"status"`
	IsSuccess bool   `json:"isSuccess"`
	Message   string `json:"message"`
	Msg       string `json:"msg"`
}

func (r apiResp) ErrMessage() string {
	if r.Message != "" {
		return r.Message
	}
	if r.Msg != "" {
		return r.Msg
	}
	return ""
}

type listResp struct {
	apiResp
	Data listData `json:"data"`
}

type listData struct {
	Children []dentry `json:"children"`
}

type dentry struct {
	DentryType      string `json:"dentryType"`
	DentryUUID      string `json:"dentryUuid"`
	Name            string `json:"name"`
	FileSize        int64  `json:"fileSize"`
	CreatedTime     int64  `json:"createdTime"`
	UpdatedTime     int64  `json:"updatedTime"`
	ContentType     string `json:"contentType"`
	Extension       string `json:"extension"`
	DentryStatistic struct {
		ChildrenCount int `json:"childrenCount"`
	} `json:"dentryStatistic"`
	URL struct {
		PCChildAppPreviewURL string `json:"pcChildAppPreviewUrl"`
		PCChildAppURL        string `json:"pcChildAppUrl"`
	} `json:"url"`
}

type downloadResp struct {
	apiResp
	Data downloadData `json:"data"`
}

type downloadData struct {
	OSSURLPreSignatureInfo struct {
		PreSignURLs []string `json:"preSignUrls"`
	} `json:"ossUrlPreSignatureInfo"`
}

type Object struct {
	model.Object
	DentryType  string
	ContentType string
	Extension   string
	PreviewURL  string
	EditURL     string
}
