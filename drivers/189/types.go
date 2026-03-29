package _189

type LoginResp struct {
	Msg    string `json:"msg"`
	Result int    `json:"result"`
	ToUrl  string `json:"toUrl"`
}

type Error struct {
	ErrorCode string `json:"errorCode"`
	ErrorMsg  string `json:"errorMsg"`
}

type File struct {
	Id         int64  `json:"id"`
	LastOpTime string `json:"lastOpTime"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Icon       struct {
		SmallUrl string `json:"smallUrl"`
		//LargeUrl string `json:"largeUrl"`
	} `json:"icon"`
	Url string `json:"url"`
}

type Folder struct {
	Id         int64  `json:"id"`
	LastOpTime string `json:"lastOpTime"`
	Name       string `json:"name"`
}

type Files struct {
	ResCode    int    `json:"res_code"`
	ResMessage string `json:"res_message"`
	FileListAO struct {
		Count      int      `json:"count"`
		FileList   []File   `json:"fileList"`
		FolderList []Folder `json:"folderList"`
	} `json:"fileListAO"`
}

type UploadUrlsResp struct {
	Code       string          `json:"code"`
	UploadUrls map[string]Part `json:"uploadUrls"`
}

type Part struct {
	RequestURL    string `json:"requestURL"`
	RequestHeader string `json:"requestHeader"`
}

type Rsa struct {
	Expire int64  `json:"expire"`
	PkId   string `json:"pkId"`
	PubKey string `json:"pubKey"`
}

type Down struct {
	ResCode         int    `json:"res_code"`
	ResMessage      string `json:"res_message"`
	FileDownloadUrl string `json:"fileDownloadUrl"`
}

type DownResp struct {
	ResCode         int    `json:"res_code"`
	ResMessage      string `json:"res_message"`
	FileDownloadUrl string `json:"downloadUrl"`
}

type CapacityResp struct {
	ResCode           int    `json:"res_code"`
	ResMessage        string `json:"res_message"`
	Account           string `json:"account"`
	CloudCapacityInfo struct {
		FreeSize     int64 `json:"freeSize"`
		MailUsedSize int64 `json:"mail189UsedSize"`
		TotalSize    int64 `json:"totalSize"`
		UsedSize     int64 `json:"usedSize"`
	} `json:"cloudCapacityInfo"`
	FamilyCapacityInfo struct {
		FreeSize  int64 `json:"freeSize"`
		TotalSize int64 `json:"totalSize"`
		UsedSize  int64 `json:"usedSize"`
	} `json:"familyCapacityInfo"`
	TotalSize uint64 `json:"totalSize"`
}
