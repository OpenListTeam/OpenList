package alidoc

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
	DentryType       string `json:"dentryType"`
	DentryUUID       string `json:"dentryUuid"`
	ParentDentryUUID string `json:"parentDentryUuid"`
	Name             string `json:"name"`
	Path             string `json:"path"`
	FileSize         int64  `json:"fileSize"`
	CreatedTime      int64  `json:"createdTime"`
	UpdatedTime      int64  `json:"updatedTime"`
	ContentType      string `json:"contentType"`
	Extension        string `json:"extension"`
	DentryStatistic  struct {
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

type uploadInfoResp struct {
	apiResp
	Data uploadInfoData `json:"data"`
}

type uploadInfoData struct {
	CurrentTimestamp         int64                  `json:"currentTimestamp"`
	FileUploadProtocolConfig uploadProtocolConfig   `json:"fileUploadProtocolConfig"`
	STSSignatureInfo         uploadSTSSignatureInfo `json:"stsSignatureInfo"`
	UploadKey                string                 `json:"uploadKey"`
	UploadType               string                 `json:"uploadType"`
}

type uploadProtocolConfig struct {
	MinPartSize int64 `json:"minPartSize"`
}

type uploadSTSSignatureInfo struct {
	AccelerateCname       string `json:"accelerateCname"`
	AccessKeyID           string `json:"accessKeyId"`
	AccessKeySecret       string `json:"accessKeySecret"`
	AccessToken           string `json:"accessToken"`
	AccessTokenExpiration int64  `json:"accessTokenExpiration"`
	Bucket                string `json:"bucket"`
	Cname                 string `json:"cname"`
	EndPoint              string `json:"endPoint"`
	ObjectKey             string `json:"objectKey"`
}
