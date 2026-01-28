package flash_transfer

type ReqInfo struct {
	ParentId        string  `json:"parent_id"`
	ReqDepth        int     `json:"req_depth"`
	Count           int     `json:"count"`
	PaginationInfo  *string `json:"pagination_info"` // 可以没有值
	FilterCondition struct {
		FileCategory int `json:"file_category"`
	} `json:"filter_condition"`
	SortConditions []struct {
		SortField int `json:"sort_field"`
		SortOrder int `json:"sort_order"`
	} `json:"sort_conditions"`
}

type DownloadInfo struct {
	BatchId string `json:"batch_id"`
	Scene   struct {
		BusinessType int `json:"business_type"`
		AppType      int `json:"app_type"`
		SceneType    int `json:"scene_type"` // 硬编码！！  4 / 22 / 5
	} `json:"scene"`
	IndexNode struct {
		FileUUid string `json:"file_uuid"`
	} `json:"index_node"`
	UrlType       int `json:"url_type"` // 2
	DownloadScene int `json:"download_scene"`
}

type FileFolderRequests struct {
	FilesetId           string    `json:"fileset_id"`
	ReqInfos            []ReqInfo `json:"req_infos"`
	SupportFolderStatus bool      `json:"support_folder_status"` // true
	SceneType           int       `json:"scene_type"`            // web !! 硬编 103 ！！
}

type CompressedFolderRequests struct {
	FilesetId string `json:"fileset_id"`
	CliFileId string `json:"cli_fileid"`
	ReqInfos  []struct {
		ParentId string `json:"parent_id"`
		ReqDepth int    `json:"req_depth"`
		Count    int    `json:"count"`
	} `json:"req_infos"`
	SceneType int `json:"scene_type"`
}

type DownloadUrlRequests struct {
	ReqHead struct {
		Agent int `json:"agent"`
	} `json:"req_head"`
	DownloadInfo []DownloadInfo `json:"download_info"`
	SceneType    int            `json:"scene_type"`
}
