package cloudflare_imgbed

import (
	"fmt"
	"path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

const listPageSize = 1000

// ListResponse 列表接口响应
type ListResponse struct {
	Files       []FileItem `json:"files"`
	Directories []string   `json:"directories"`
}

type FileItem struct {
	Name     string                 `json:"name"`
	Metadata map[string]interface{} `json:"metadata"` // 存储文件大小、哈希、时间戳等
}

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// standardUploadResp 标准上传成功返回的数组
type standardUploadResp []struct {
	Src string `json:"src"`
}

// hfGetUrlResp 获取 HF 直传授权地址的响应
type hfGetUrlResp struct {
	Success       bool          `json:"success"`
	FullID        string        `json:"fullId"`
	FilePath      string        `json:"filePath"`
	ChannelName   string        `json:"channelName"`
	Repo          string        `json:"repo"`
	NeedsLfs      bool          `json:"needsLfs"`      // 是否需要进行 LFS 物理上传
	AlreadyExists bool          `json:"alreadyExists"` // 是否秒传成功
	Oid           string        `json:"oid"`           // Git LFS 对象 ID (SHA256)
	UploadAction  *UploadAction `json:"uploadAction"`
}

type UploadAction struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header"`
}

type hfCommitResp struct {
	Success bool   `json:"success"`
	Src     string `json:"src"`
	FileUrl string `json:"fileUrl"`
	FullID  string `json:"fullId"`
}

// 辅助函数：从 map 中安全提取字符串/数值
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

func parseFile(item FileItem) *model.Object {
	name := path.Base(item.Name)
	var size int64
	var modTime time.Time

	if item.Metadata != nil {
		size = getInt64(item.Metadata, "FileSizeBytes", "File-Size")
		ts := getInt64(item.Metadata, "TimeStamp")
		if ts > 0 {
			modTime = time.UnixMilli(ts)
		}
	}

	return &model.Object{
		Path:     item.Name,
		Name:     name,
		Size:     size,
		Modified: modTime,
		IsFolder: false,
	}
}
