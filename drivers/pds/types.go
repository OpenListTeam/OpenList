package pds

import (
	"path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type fileItem struct {
	DriveID      string `json:"drive_id"`
	FileID       string `json:"file_id"`
	ParentFileID string `json:"parent_file_id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	FileSize     int64  `json:"size"`
	UpdatedAt    string `json:"updated_at"`
	CreatedAt    string `json:"created_at"`
	DownloadURL  string `json:"download_url"`
	Thumbnail    string `json:"thumbnail"`
}

func (f fileItem) ModTime() time.Time {
	for _, raw := range []string{f.UpdatedAt, f.CreatedAt} {
		if raw == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return t
		}
	}
	return time.Now()
}

type listFilesResp struct {
	Items      []fileItem `json:"items"`
	NextMarker string     `json:"next_marker"`
}

type createFileResp struct {
	DriveID      string `json:"drive_id"`
	FileID       string `json:"file_id"`
	UploadID     string `json:"upload_id"`
	Name         string `json:"name"`
	FileName     string `json:"file_name"`
	PartInfoList []struct {
		PartNumber int    `json:"part_number"`
		UploadURL  string `json:"upload_url"`
	} `json:"part_info_list"`
}

func (f createFileResp) toObj() model.Obj {
	name := f.FileName
	if name == "" {
		name = f.Name
	}
	return &model.Object{
		ID:       f.FileID,
		Name:     name,
		Modified: time.Now(),
		IsFolder: true,
	}
}

type copyMoveResp struct {
	DriveID string `json:"drive_id"`
	FileID  string `json:"file_id"`
}

type driveResp struct {
	DriveID   string `json:"drive_id"`
	UsedSize  int64  `json:"used_size"`
	TotalSize int64  `json:"total_size"`
}

func toObjs(items []fileItem, parentPath string) []model.Obj {
	objs := make([]model.Obj, 0, len(items))
	for _, item := range items {
		obj := item.toObj()
		if setter, ok := obj.(model.SetPath); ok {
			setter.SetPath(path.Join(parentPath, item.Name))
		}
		objs = append(objs, obj)
	}
	return objs
}

func (f fileItem) toObj() model.Obj {
	size := f.FileSize
	if f.Type == "folder" {
		size = 0
	}
	return &model.ObjThumb{
		Object: model.Object{
			ID:       f.FileID,
			Name:     f.Name,
			Size:     size,
			Modified: f.ModTime(),
			IsFolder: f.Type == "folder",
		},
		Thumbnail: model.Thumbnail{Thumbnail: f.Thumbnail},
	}
}
