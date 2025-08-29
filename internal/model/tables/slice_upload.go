package tables

const (
	//SliceUploadStatusWaiting 等待上传
	SliceUploadStatusWaiting = iota
	// SliceUploadStatusUploading 正在上传
	SliceUploadStatusUploading
	// SliceUploadStatusCancelled 取消上传
	SliceUploadStatusCancelled
	// SliceUploadStatusComplete 上传完成
	SliceUploadStatusComplete
	// SliceUploadStatusFailed 上传失败
	SliceUploadStatusFailed
	// SliceUploadStatusProxyComplete 成功上传到代理服务，等待上传到网盘
	SliceUploadStatusProxyComplete
)

// SliceUpload 分片上传数据表
type SliceUpload struct {
	Base
	PreupID           string `json:"preup_id"`                                                     // 网盘返回的预上传id
	SliceSize         int64  `json:"slice_size"`                                                   // 分片大小，单位：字节
	DstID             string `json:"dst_id"`                                                       // 目标文件夹ID，部分网盘需要
	DstPath           string `json:"dst_path"`                                                     // 挂载的父文件夹路径
	ActualPath        string `json:"actual_path"`                                                  //网盘真实父文件夹路径，不同的网盘，这个值可能相同，比如有相同的目录的两个网盘
	Name              string `json:"name"`                                                         // 文件名
	Size              int64  `json:"size"`                                                         // 文件大小
	TmpFile           string `json:"tmp_file"`                                                     //不支持分片上传的文件临时文件路径
	HashMd5           string `json:"hash_md5"`                                                     // md5
	HashMd5256KB      string `json:"hash_md5_256kb" gorm:"column:hash_md5_256kb;type:varchar(32)"` // md5256KB
	HashSha1          string `json:"hash_sha1"`                                                    // sha1
	SliceHash         string `json:"slice_hash"`                                                   // 分片hash
	SliceCnt          uint   `json:"slice_cnt"`                                                    // 分片数量
	SliceUploadStatus []byte `json:"slice_upload_status"`                                          //分片上传状态，对应位置1表示分片已上传
	Server            string `json:"server"`                                                       // 上传服务器
	Status            int    `json:"status"`                                                       //上传状态
	Message           string `json:"message"`                                                      // 失败错误信息
	Overwrite         bool   `json:"overwrite"`                                                    // 是否覆盖同名文件
	UserID            uint   `json:"user_id"`                                                      //用户id
	AsTask            bool   `json:"as_task"`
}

// IsSliceUploaded 判断第i个分片是否已上传
func IsSliceUploaded(status []byte, i int) bool {
	return status[i/8]&(1<<(i%8)) != 0
}

// SetSliceUploaded 标记第i个分片已上传
func SetSliceUploaded(status []byte, i int) {
	status[i/8] |= 1 << (i % 8)
}

// IsAllSliceUploaded 是否全部上传完成
func IsAllSliceUploaded(status []byte, sliceCnt uint) bool {
	for i := range sliceCnt {
		if status[i/8]&(1<<(i%8)) == 0 {
			return false
		}
	}
	return true
}
