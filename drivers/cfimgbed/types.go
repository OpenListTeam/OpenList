package cfimgbed

import (
	"fmt"
	"path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// File represents a file object parsed from the CFImgBed List API response.
// It implements the model.Obj interface.
type File struct {
	Path     string
	Name_    string
	Size_    int64
	ModTime_ time.Time
	Mime_    string
}

func (f *File) GetPath() string         { return f.Path }
func (f *File) GetName() string         { return f.Name_ }
func (f *File) ModTime() time.Time      { return f.ModTime_ }
func (f *File) CreateTime() time.Time   { return f.ModTime_ }
func (f *File) GetSize() int64          { return f.Size_ }
func (f *File) IsDir() bool             { return false }
func (f *File) GetID() string           { return f.Path }
func (f *File) GetHash() utils.HashInfo { return utils.HashInfo{} }

// Dir represents a directory object parsed from the CFImgBed List API response.
// It implements the model.Obj interface.
type Dir struct {
	Path  string
	Name_ string
}

func (d *Dir) GetPath() string         { return d.Path }
func (d *Dir) GetName() string         { return d.Name_ }
func (d *Dir) ModTime() time.Time      { return time.Time{} }
func (d *Dir) CreateTime() time.Time   { return time.Time{} }
func (d *Dir) GetSize() int64          { return 0 }
func (d *Dir) IsDir() bool             { return true }
func (d *Dir) GetID() string           { return d.Path }
func (d *Dir) GetHash() utils.HashInfo { return utils.HashInfo{} }

// Compile-time checks to ensure File and Dir implement model.Obj.
var _ model.Obj = (*File)(nil)
var _ model.Obj = (*Dir)(nil)

// ListResponse represents the JSON structure returned by the CFImgBed List API.
type ListResponse struct {
	Files       []FileItem `json:"files"`
	Directories []string   `json:"directories"`
}

// FileItem represents a single file entry in the List API response.
// Metadata uses map[string]interface{} because the actual API returns mixed types:
//   - TimeStamp: integer (e.g. 1774910085474) in newer versions
//   - FileSizeBytes: integer (e.g. 3936071)
//   - FileSize: string (e.g. "3.75") — human-readable size
//   - FileType: string (e.g. "audio/mpeg")
//   - Legacy fields may use string values for numbers
type FileItem struct {
	Name     string                 `json:"name"`
	Metadata map[string]interface{} `json:"metadata"`
}

// getString safely extracts a string value from metadata, trying key in order.
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

// getInt64 safely extracts an int64 value from metadata, trying key in order.
// Supports string, float64 (JSON number), and int64 types.
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

// parseFile converts an API FileItem to a *File model.Obj.
// It tries multiple key names for each field to handle different API versions:
//   - Size: FileSizeBytes (int) > File-Size (string)
//   - MIME: FileType > File-Mime
//   - Time: TimeStamp (handles both int and string)
func parseFile(item FileItem) *File {
	name := path.Base(item.Name)
	var size int64
	var modTime time.Time
	var mime string

	if item.Metadata != nil {
		// Try FileSizeBytes (int) first, fall back to File-Size (string)
		size = getInt64(item.Metadata, "FileSizeBytes", "File-Size")

		// Try FileType first, fall back to File-Mime
		mime = getString(item.Metadata, "FileType", "File-Mime")

		// TimeStamp may be int or string depending on API version
		ts := getInt64(item.Metadata, "TimeStamp")
		if ts > 0 {
			modTime = time.UnixMilli(ts)
		}
	}

	return &File{
		Path:     item.Name,
		Name_:    name,
		Size_:    size,
		ModTime_: modTime,
		Mime_:    mime,
	}
}

// parseDir converts a directory path string from the API to a *Dir model.Obj.
func parseDir(dirPath string) *Dir {
	return &Dir{
		Path:  dirPath,
		Name_: path.Base(dirPath),
	}
}
