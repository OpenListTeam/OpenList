package flash_transfer

type BaseResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	Cost    int    `json:"cost"`
	Message string `json:"message"`
	RetCode int    `json:"retcode"`
}
type Thumbnail struct {
	Sha1 string `json:"sha1"`
	Urls []struct {
		Url  string `json:"url"`
		Spec int    `json:"spec"`
	} `json:"urls"`
	Id   string `json:"id"`
	Md5  string `json:"md5"`
	Size string `json:"size"`
}

type PhysicalInfo struct {
	DownloadLimitStatus int    `json:"download_limit_status"`
	Url                 string `json:"url"`
	Id                  string `json:"id"`
	Processing          string `json:"processing"`
	Status              int    `json:"status"` // 2完成上传 可以下载
	IsUnzipped          bool   `json:"is_unzipped"`
}
type FileInfo struct {
	FileCount        int          `json:"file_count"`
	Thumbnail        Thumbnail    `json:"thumbnail"`
	Physical         PhysicalInfo `json:"physical"`
	Path             string       `json:"path"`
	SrvFileid        string       `json:"srv_fileid"`
	SrvParentFileid  string       `json:"srv_parent_fileid"`
	SafeStatus       int          `json:"safe_status"`
	ParentId         string       `json:"parent_id"`
	FileSha1         string       `json:"file_sha1"`
	FilePhysicalSize string       `json:"file_physical_size"`
	FileMd5          string       `json:"file_md5"`
	Name             string       `json:"name"`
	IsDir            bool         `json:"is_dir"`
	CliFileIndex     int          `json:"cli_file_index"`
	FileType         int          `json:"file_type"`
	FilesetId        string       `json:"fileset_id"`
	FileSize         string       `json:"file_size"`
	CliFileid        string       `json:"cli_fileid"`
}

type FileListResponse struct {
	BaseResponse
	Data struct {
		FileLists []struct {
			PaginationInfo string     `json:"pagination_info"`
			ParentId       string     `json:"parent_id"`
			IsEnd          bool       `json:"is_end"`
			Depth          int        `json:"depth"` // 默认都是1的
			FileList       []FileInfo `json:"file_list"`
		} `json:"file_lists"`
	} `json:"data"`
}

type CompressedFileFolderResponse struct {
	BaseResponse
	Data struct {
		RemainingTime string `json:"remaining_time"`
		RemainingMsg  string `json:"remaining_msg"`
		FileLists     []struct {
			ParentId       string     `json:"parent_id"`
			PaginationInfo string     `json:"pagination_info"`
			FileList       []FileInfo `json:"file_list"`
			Depth          int        `json:"depth"` // 依旧默认1 其它的报错直接
			IsEnd          bool       `json:"is_end"`
		} `json:"file_lists"`
		TotalFileCount string `json:"total_file_count"`
		TotalFileSize  string `json:"total_file_size"`
	} `json:"data"`
}

type DownloadUrlResponse struct {
	BaseResponse
	Data struct {
		DownloadRsp []struct {
			BatchId string `json:"batch_id"`
			Url     string `json:"url"`
			RetMsg  string `json:"ret_msg"`
			RetCode string `json:"ret_code"`
			Domain  string `json:"domain"`
		} `json:"download_rsp"`
	} `json:"data"`
}
