package micloud

// 由于json包在类型定义中没有直接使用，但可能在方法中需要，我们保留它
// 实际上json包在MiCloudClient的实现中使用了，所以保留

// File 小米云盘文件结构
type File struct {
	Sha1       string `json:"sha1"`
	ModifyTime uint   `json:"modifyTime"`
	Size       int64  `json:"size"`
	CreateTime uint   `json:"createTime"`
	Name       string `json:"name"`
	Id         string `json:"id"`
	Type       string `json:"type"` // "file" or "folder"
	Revision   string `json:"revision"`
	IsActive   bool   `json:"isActive"`
}

// API响应结构
type Msg struct {
	Result    string `json:"result"`
	Retryable bool   `json:"retryable"`
	Code      int    `json:"code"`
	Data      struct {
		HasMore bool   `json:"has_more"`
		List    []File `json:"list"`
	} `json:"data"`
}

// 上传相关结构
type UploadJson struct {
	Content UploadContent `json:"content"`
}

type UploadContent struct {
	Name     string      `json:"name"`
	ParentId string      `json:"parentId"`
	Storage  interface{} `json:"storage"`
}

// Detailed storage payloads used by MiCloud KSS flow
type UploadStorage struct {
	Size     int64       `json:"size"`
	Sha1     string      `json:"sha1"`
	Kss      interface{} `json:"kss"`
	UploadId string      `json:"uploadId"`
	Exists   bool        `json:"exists"`
}

type UploadExistedStorage struct {
	UploadId string `json:"uploadId"`
	Exists   bool   `json:"exists"`
}

type UploadKss struct {
	BlockInfos []BlockInfo `json:"block_infos"`
}

type Kss struct {
	Stat            string              `json:"stat"`
	NodeUrls        interface{}         `json:"node_urls"`
	SecureKey       string              `json:"secure_key"`
	ContentCacheKey string              `json:"contentCacheKey"`
	FileMeta        string              `json:"file_meta"`
	CommitMetas     []map[string]string `json:"commit_metas"`
}

type BlockInfo struct {
	Blob struct{} `json:"blob"`
	Sha1 string   `json:"sha1"`
	Md5  string   `json:"md5"`
	Size int64    `json:"size"`
}

// 常量定义
const (
	BaseUri      = "https://i.mi.com"
	GetFiles     = BaseUri + "/drive/user/files/%s?jsonpCallback=callback"
	GetFolders   = BaseUri + "/drive/user/folders/%s/children"
	GetDirectDL  = BaseUri + "/drive/v2/user/files/download"
	AutoRenewal  = BaseUri + "/status/setting?type=AutoRenewal&inactiveTime=10&_dc=%d"
	CreateFile   = BaseUri + "/drive/user/files/create"
	UploadFile   = BaseUri + "/drive/user/files"
	DeleteFiles  = BaseUri + "/drive/v2/user/records/filemanager"
	CreateFolder = BaseUri + "/drive/v2/user/folders/create"
	ChunkSize    = 4194304 // 4MB
)
