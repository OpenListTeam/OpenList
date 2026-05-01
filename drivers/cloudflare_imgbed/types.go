package cloudflare_imgbed

import (
	"fmt"
	"path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// File 表示从 CFImgBed 列表 API 响应中解析出的文件对象，实现 model.Obj 接口。
type File struct {
	path    string    // 文件相对路径，如 "example/image.jpg"
	name    string    // 显示名称（路径最后一段），如 "image.jpg"
	size    int64     // 文件大小（字节）
	modTime time.Time // 最后修改时间（从 Unix 毫秒时间戳转换而来）
	mime    string    // MIME 类型，如 "image/jpeg"
}

func (f *File) GetPath() string         { return f.path }
func (f *File) GetName() string         { return f.name }
func (f *File) ModTime() time.Time      { return f.modTime }
func (f *File) CreateTime() time.Time   { return f.modTime }
func (f *File) GetSize() int64          { return f.size }
func (f *File) IsDir() bool             { return false }
func (f *File) GetID() string           { return f.path }
func (f *File) GetHash() utils.HashInfo { return utils.HashInfo{} }

// Dir 表示从 CFImgBed 列表 API 响应中解析出的目录对象，实现 model.Obj 接口。
type Dir struct {
	path string // 目录相对路径，如 "example/subfolder"
	name string // 显示名称（路径最后一段），如 "subfolder"
}

func (d *Dir) GetPath() string         { return d.path }
func (d *Dir) GetName() string         { return d.name }
func (d *Dir) ModTime() time.Time      { return time.Time{} }
func (d *Dir) CreateTime() time.Time   { return time.Time{} }
func (d *Dir) GetSize() int64          { return 0 }
func (d *Dir) IsDir() bool             { return true }
func (d *Dir) GetID() string           { return d.path }
func (d *Dir) GetHash() utils.HashInfo { return utils.HashInfo{} }

// 编译时检查 File 和 Dir 是否完整实现 model.Obj 接口。
var _ model.Obj = (*File)(nil)
var _ model.Obj = (*Dir)(nil)

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

// getString 从 metadata 中安全提取字符串值，按 keys 顺序依次尝试。
// 支持 string 和 float64（JSON 数字反序列化后的默认类型）两种输入。
func getString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch val := v.(type) {
			case string:
				return val
			case float64:
				return strconv.FormatInt(int64(val), 10)
			default:
				return fmt.Sprintf("%v", val)
			}
		}
	}
	return ""
}

// getInt64 从 metadata 中安全提取 int64 值，按 keys 顺序依次尝试。
// 同时兼容 string、float64（JSON 数字）和 int64 三种反序列化类型，
// 确保在不同 API 版本下均能正确解析。
func getInt64(m map[string]interface{}, keys ...string) int64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch val := v.(type) {
			case string:
				n, _ := strconv.ParseInt(val, 10, 64)
				return n
			case float64:
				return int64(val)
			case int64:
				return val
			}
		}
	}
	return 0
}

// parseFile 将 API 返回的 FileItem 转换为 *File 对象。
// 字段提取策略（兼容新旧 API 版本）：
//   - 文件大小：优先取 FileSizeBytes（int），回退到 File-Size（string）
//   - MIME 类型：优先取 FileType，回退到 File-Mime
//   - 修改时间：取 TimeStamp（同时处理 int 和 string 两种格式）
func parseFile(item FileItem) *File {
	name := path.Base(item.Name)
	var size int64
	var modTime time.Time
	var mime string

	if item.Metadata != nil {
		size = getInt64(item.Metadata, "FileSizeBytes", "File-Size")
		mime = getString(item.Metadata, "FileType", "File-Mime")
		ts := getInt64(item.Metadata, "TimeStamp")
		if ts > 0 {
			modTime = time.UnixMilli(ts)
		}
	}

	return &File{
		path:    item.Name,
		name:    name,
		size:    size,
		modTime: modTime,
		mime:    mime,
	}
}

// parseDir 将 API 返回的目录路径字符串转换为 *Dir 对象。
// 显示名称取路径的最后一段（即最深层目录名）。
func parseDir(dirPath string) *Dir {
	return &Dir{
		path: dirPath,
		name: path.Base(dirPath),
	}
}
