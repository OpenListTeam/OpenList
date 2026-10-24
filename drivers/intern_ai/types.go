package intern_ai

// ----- Generic API envelope -----

// apiResp is the common response envelope of the InternAI platform.
type apiResp struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Success bool   `json:"success"`
	Data    struct {
		Files       []fileItem `json:"files"`
		NextToken   string     `json:"next_page_token"`
		Total       int64      `json:"total"`
		HasMore     bool       `json:"has_more"`
		URLs        []urlItem  `json:"urls"`
		Results     []checkRes `json:"results"`
		Names       []string   `json:"names"`
		UsedGb      float64    `json:"used_gb"`
		TotalGb     float64    `json:"total_gb"`
		UsedBytes   int64      `json:"used_bytes"`
		TotalBytes  int64      `json:"total_bytes"`
		CloudUsedGb float64    `json:"cloud_used_gb"`
		CloudTotal  float64    `json:"cloud_total_gb"`
	} `json:"data"`
}

// fileItem is one entry of the cloud-disk/list response.
type fileItem struct {
	// FileKey is the full path of the entry, e.g. "/docs/readme.txt"
	FileKey     string `json:"file_key"`
	FileName    string `json:"file_name"`
	IsDir       bool   `json:"is_dir"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	ModifiedAt  string `json:"modified_at"`
	// CreatedAt is returned by some deployments
	CreatedAt string `json:"created_at"`
	// CloudFileStatus is a UI hint ("1" creating, "2" renaming) and ignored here
	CloudFileStatus string `json:"cloud_file_status"`
}

// urlItem is one entry of download-url / upload-url responses.
type urlItem struct {
	FileKey     string `json:"file_key"`
	FileName    string `json:"file_name"`
	DownloadURL string `json:"download_url"`
	UploadURL   string `json:"upload_url"`
	Skipped     bool   `json:"skipped"`
}

// checkRes is one entry of the check (name conflict) response.
type checkRes struct {
	FileKey string `json:"file_key"`
	Exists  bool   `json:"exists"`
}
