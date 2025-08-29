package reqres

import "github.com/OpenListTeam/OpenList/v4/internal/model"

// PreupReq 预上传请求
type PreupReq struct {
	Path      string     `json:"path"` // 上传到的挂载路径
	Name      string     `json:"name"`
	Size      int64      `json:"size"`
	Hash      model.Hash `json:"hash"`
	Overwrite bool       `json:"overwrite"` // 是否覆盖同名文件
	AsTask    bool       `json:"as_task"`
}

// PreupResp 预上传响应
type PreupResp struct {
	UploadID          uint   `json:"upload_id"`           // 上传ID，不是网盘返回的，是本地数据的id
	SliceSize         int64  `json:"slice_size"`          //分片大小，单位：字节
	SliceCnt          uint   `json:"slice_cnt"`           // 分片数量
	SliceUploadStatus []byte `json:"slice_upload_status"` // 分片上传状态
	Reuse             bool   `json:"reuse"`               //是否秒传
}

// UploadSliceReq 上传分片请求
type UploadSliceReq struct {
	UploadID  uint   `json:"upload_id"`  // 上传ID，不是网盘返回的，是本地数据的id
	SliceHash string `json:"slice_hash"` // 分片hash，如果是第一个分片，则需包含所有分片hash，用","分割
	SliceNum  uint   `json:"slice_num"`  // 分片序号
}

// UploadSliceCompleteReq 分片上传完成请求
type UploadSliceCompleteReq struct {
	UploadID uint `json:"upload_id"` // 上传ID，不是网盘返回的，是本地数据的id
}

// UploadSliceCompleteResp 分片上传完成响应
type UploadSliceCompleteResp struct {
	UploadID          uint   `json:"upload_id"`           // 上传ID，不是网盘返回的，是本地数据的id
	SliceUploadStatus []byte `json:"slice_upload_status"` // 分片上传状态
	Complete          uint   `json:"complete"`            //完成状态 0 未完成，分片缺失 1 完成 2 成功上传到代理服务
}
