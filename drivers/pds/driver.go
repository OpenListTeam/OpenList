package pds

import (
	"context"
	"errors"
	"path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type PDS struct {
	model.Storage
	Addition
	client *client
}

func (d *PDS) Config() driver.Config {
	return config
}

func (d *PDS) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *PDS) Init(ctx context.Context) error {
	d.client = newClient(&d.Addition, func() {
		op.MustSaveDriverStorage(d)
	})
	if d.RootFolderID == "" {
		d.RootFolderID = "root"
	}
	if d.DriveID == "" {
		return errors.New("drive_id is required")
	}
	if d.DomainID == "" {
		return errors.New("domain_id is required")
	}
	if d.AccessToken == "" && d.RefreshToken == "" {
		return errors.New("access_token or refresh_token is required")
	}
	return nil
}

func (d *PDS) Drop(ctx context.Context) error {
	return nil
}

func (d *PDS) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	parentID := d.fileID(dir)
	var all []fileItem
	marker := ""
	for {
		var resp listFilesResp
		err := d.client.post(ctx, "/v2/file/list", map[string]any{
			"drive_id":               d.DriveID,
			"parent_file_id":         parentID,
			"limit":                  100,
			"marker":                 marker,
			"order_by":               "updated_at",
			"order_direction":        "DESC",
			"fields":                 "*",
			"url_expire_sec":         7200,
			"include_handover_drive": true,
		}, &resp)
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		if resp.NextMarker == "" {
			break
		}
		marker = resp.NextMarker
	}
	parentPath := dir.GetPath()
	if parentPath == "" {
		parentPath = "/"
	}
	return toObjs(all, parentPath), nil
}

func (d *PDS) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	item, err := d.getFile(ctx, d.fileID(file))
	if err != nil {
		return nil, err
	}
	if item.DownloadURL == "" {
		return nil, errs.NotFile
	}
	exp := 2 * time.Hour
	return &model.Link{URL: item.DownloadURL, Expiration: &exp}, nil
}

func (d *PDS) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	var out createFileResp
	err := d.client.post(ctx, "/v2/file/create", map[string]any{
		"drive_id":        d.DriveID,
		"parent_file_id":  d.fileID(parentDir),
		"name":            dirName,
		"type":            "folder",
		"check_name_mode": "auto_rename",
	}, &out)
	if err != nil {
		return nil, err
	}
	return withParentPath(parentDir.GetPath(), out.toObj()), nil
}

func (d *PDS) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	var out copyMoveResp
	err := d.client.post(ctx, "/v2/file/move", map[string]any{
		"drive_id":          d.DriveID,
		"file_id":           d.fileID(srcObj),
		"to_drive_id":       d.DriveID,
		"to_parent_file_id": d.fileID(dstDir),
		"check_name_mode":   "auto_rename",
	}, &out)
	if err != nil {
		return nil, err
	}
	obj, err := d.getFileObj(ctx, out.FileID)
	if err != nil {
		return nil, err
	}
	return withParentPath(dstDir.GetPath(), obj), nil
}

func (d *PDS) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	var out fileItem
	err := d.client.post(ctx, "/v2/file/update", map[string]any{
		"drive_id":        d.DriveID,
		"file_id":         d.fileID(srcObj),
		"name":            newName,
		"check_name_mode": "auto_rename",
	}, &out)
	if err != nil {
		return nil, err
	}
	return withParentPath(path.Dir(srcObj.GetPath()), out.toObj()), nil
}

func (d *PDS) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	var out copyMoveResp
	err := d.client.post(ctx, "/v2/file/copy", map[string]any{
		"drive_id":          d.DriveID,
		"file_id":           d.fileID(srcObj),
		"to_drive_id":       d.DriveID,
		"to_parent_file_id": d.fileID(dstDir),
		"check_name_mode":   "auto_rename",
	}, &out)
	if err != nil {
		return nil, err
	}
	obj, err := d.getFileObj(ctx, out.FileID)
	if err != nil {
		return nil, err
	}
	return withParentPath(dstDir.GetPath(), obj), nil
}

func (d *PDS) Remove(ctx context.Context, obj model.Obj) error {
	return d.client.post(ctx, "/v2/recyclebin/trash", map[string]any{
		"drive_id": d.DriveID,
		"file_id":  d.fileID(obj),
	}, nil)
}

func (d *PDS) GetRoot(ctx context.Context) (model.Obj, error) {
	return &model.Object{
		ID:       d.RootFolderID,
		Path:     "/",
		Name:     "root",
		Modified: d.Modified,
		IsFolder: true,
		Mask:     model.Locked,
	}, nil
}

func (d *PDS) Get(ctx context.Context, path string) (model.Obj, error) {
	if path == "/" || path == "" {
		return d.GetRoot(ctx)
	}
	return d.getByPath(ctx, path)
}

func (d *PDS) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	var drive driveResp
	err := d.client.post(ctx, "/v2/drive/get", map[string]any{
		"drive_id": d.DriveID,
	}, &drive)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: drive.TotalSize,
			UsedSpace:  drive.UsedSize,
		},
	}, nil
}

var _ driver.Driver = (*PDS)(nil)
var _ driver.Getter = (*PDS)(nil)
var _ driver.GetRooter = (*PDS)(nil)
var _ driver.PutResult = (*PDS)(nil)
var _ driver.MkdirResult = (*PDS)(nil)
var _ driver.MoveResult = (*PDS)(nil)
var _ driver.RenameResult = (*PDS)(nil)
var _ driver.CopyResult = (*PDS)(nil)
var _ driver.Remove = (*PDS)(nil)
var _ driver.WithDetails = (*PDS)(nil)
