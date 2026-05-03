package cloudflare_imgbed

// listPageSize 定义每次向 API 请求的最大条目数。
// 采用内部分页循环拉取，以防止单目录文件过多导致 API 响应超时或内存异常。
const listPageSize = 1000

// ListResponse 表示 CFImgBed 列表 API 返回的 JSON 结构。
type ListResponse struct {
	Files       []FileItem `json:"files"`
	Directories []string   `json:"directories"`
}

// FileItem 表示列表 API 返回的单个文件条目。
// 注意：Metadata 使用 map[string]interface{} 而非 map[string]string，
// 因为实际 API 返回的字段类型不统一：
//   - TimeStamp: 可能是整数（如 1774910085474），也可能在旧版本中是字符串
//   - FileSizeBytes: 整数（如 3936071）
//   - FileSize: 字符串（如 "3.75"）— 仅供人类阅读的格式化大小
//   - FileType: 字符串（如 "audio/mpeg"）
type FileItem struct {
	Name     string                 `json:"name"`
	Metadata map[string]interface{} `json:"metadata"`
}
