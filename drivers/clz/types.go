package clz

import "github.com/OpenListTeam/OpenList/v4/internal/model"

type Resp[T any] struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Files   []File `json:"files"`
}

type File struct {
	Fid        int64  `json:"fid"`
	FileType   string `json:"filetype"`
	FileName   string `json:"filename"`
	FileSize   int64  `json:"filesize"`
	Path       string `json:"path"`
	CreateTime string `json:"create_time"`
}

// 转换 API 对象为 OpenList 对象
func (f *File) ToObj() model.Obj {
	return &model.Object{
		ID:       string(f.Fid),
		Name:     f.FileName,
		Size:     f.FileSize,
		Modified: model.ParseTime(f.CreateTime),
		IsFolder: f.FileType == "directory",
	}
}

type VideoResp struct {
	Code        string `json:"code"`
	VideoURL    string `json:"video_url"`
	IsEncrypted bool   `json:"is_encrypted"`
}