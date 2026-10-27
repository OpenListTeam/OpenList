package ctfile

// CtFile (城通网盘) OpenAPI response structs.
// API doc: https://openapi.ctfile.com/ (REST API v1, unified POST JSON + session auth).
// All business endpoints return a unified wrapper { code, message?, ...business fields }.
// `code == 200` means success; `401` means session expired; `403` with folder_id on mkdir
// means the folder already exists.

// Resp is the unified response wrapper. Business fields are decoded via
// utils.Json to stay tolerant of the server's loosely-typed payloads (numbers
// may arrive as strings and vice-versa).
type Resp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	// folder_id / file_id / download_url / upload_url / exists / results / ...
	// are accessed through utils.Json, so we keep the raw bytes here.
}

// profileResp holds the fields returned by POST /user/info/profile.
type profileResp struct {
	UserID     int64  `json:"userid"`
	UserName   string `json:"username"`
	NickName   string `json:"nick_name"`
	GroupName  string `json:"group_name"`
	MaxFileSz  int64  `json:"max_filesize"`
}

// folderEntry is a single folder in folder/list results.
type folderEntry struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Icon string `json:"icon"`
	Date any    `json:"date"` // unix seconds or "YYYY-MM-DD HH:mm:ss"
}

// fileEntry is a single file in file/list results.
type fileEntry struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Icon    string `json:"icon"`
	ImgSrc  string `json:"imgsrc"`
	Size    int64  `json:"size"`
	Date    any    `json:"date"`
	Status  int    `json:"status"` // 1=complete, 2=incomplete (not downloadable)
}

// uploadTicket is the POST /{ns}/file/upload response.
type uploadTicket struct {
	UploadURL string `json:"upload_url"`
	Exists    int    `json:"exists"` // >0 => instant upload hit
}
