package cloudflare_imgbed

import (
	"fmt"
	"path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

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
func parseFile(item FileItem) *model.Object {
	name := path.Base(item.Name)
	var size int64
	var modTime time.Time
	// var mime string

	if item.Metadata != nil {
		size = getInt64(item.Metadata, "FileSizeBytes", "File-Size")
		// mime = getString(item.Metadata, "FileType", "File-Mime")
		ts := getInt64(item.Metadata, "TimeStamp")
		if ts > 0 {
			modTime = time.UnixMilli(ts)
		}
	}

	return &model.Object{
		Name:     name,
		Size:     size,
		Modified: modTime,
		// ID:       mime,
	}
}
