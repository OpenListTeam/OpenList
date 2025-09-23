package model

// UploadInfo 上传所需信息
type UploadInfo struct {
	SliceHashNeed    bool `json:"slice_hash_need"`     //是否需要分片哈希
	HashMd5Need      bool `json:"hash_md5_need"`       //是否需要md5
	HashMd5256KBNeed bool `json:"hash_md5_256kb_need"` //是否需要前256KB的md5
	HashSha1Need     bool `json:"hash_sha1_need"`      //是否需要sha1
}

// PreupInfo 预上传信息
type PreupInfo struct {
	PreupID   string `json:"preup_id"`   //预上传id，由网盘返回
	SliceSize int64  `json:"slice_size"` //分片大小
	Server    string `json:"server"`     //上传服务器地址
	Reuse     bool   `json:"reuse"`      //是否秒传
}
