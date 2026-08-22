package pds

import (
	"context"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func withFilePreviewParams(params map[string]any) map[string]any {
	params["url_expire_sec"] = 7200
	params["image_thumbnail_process"] = "image/resize,w_400/format,jpeg"
	params["image_url_process"] = "image/resize,w_1920/format,jpeg"
	params["video_thumbnail_process"] = "video/snapshot,t_0,f_jpg,ar_auto,w_300"
	return params
}

func (d *PDS) fileID(obj model.Obj) string {
	if obj == nil {
		return d.RootFolderID
	}
	if id := obj.GetID(); id != "" {
		return id
	}
	return d.RootFolderID
}

func withParentPath(parentPath string, obj model.Obj) model.Obj {
	if obj == nil {
		return nil
	}
	if parentPath == "" || parentPath == "." {
		parentPath = "/"
	}
	if setter, ok := obj.(model.SetPath); ok {
		setter.SetPath(path.Join(parentPath, obj.GetName()))
	}
	return obj
}

func (d *PDS) getFile(ctx context.Context, fileID string) (fileItem, error) {
	var item fileItem
	err := d.client.post(ctx, "/v2/file/get", withFilePreviewParams(map[string]any{
		"drive_id": d.DriveID,
		"file_id":  fileID,
	}), &item)
	return item, err
}

func (d *PDS) getFileObj(ctx context.Context, fileID string) (model.Obj, error) {
	item, err := d.getFile(ctx, fileID)
	if err != nil {
		return nil, err
	}
	return item.toObj(), nil
}

func (d *PDS) getByPath(ctx context.Context, rawPath string) (model.Obj, error) {
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	parentID := d.RootFolderID
	var current fileItem
	currentPath := "/"
	for _, part := range parts {
		if part == "" {
			continue
		}
		found, err := d.findChild(ctx, parentID, part)
		if err != nil {
			return nil, err
		}
		current = found
		parentID = found.FileID
		currentPath = path.Join(currentPath, found.Name)
	}
	if current.FileID == "" {
		return nil, errs.ObjectNotFound
	}
	obj := current.toObj()
	if setter, ok := obj.(model.SetPath); ok {
		setter.SetPath(currentPath)
	}
	return obj, nil
}

func (d *PDS) findChild(ctx context.Context, parentID, name string) (fileItem, error) {
	var resp listFilesResp
	err := d.client.post(ctx, "/v2/file/search", withFilePreviewParams(map[string]any{
		"drive_id": d.DriveID,
		"query":    "parent_file_id = \"" + parentID + "\" and name = \"" + escapeQueryValue(name) + "\"",
		"limit":    10,
		"fields":   "*",
	}), &resp)
	if err != nil {
		return fileItem{}, err
	}
	for _, item := range resp.Items {
		if item.Name == name {
			return item, nil
		}
	}
	return fileItem{}, errs.ObjectNotFound
}

func escapeQueryValue(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	return strings.ReplaceAll(s, "\"", "\\\"")
}
