package alidoc

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

type AliDoc struct {
	model.Storage
	Addition

	client *resty.Client
}

func (d *AliDoc) Config() driver.Config {
	return config
}

func (d *AliDoc) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *AliDoc) Init(ctx context.Context) error {
	d.Cookie = strings.TrimSpace(d.Cookie)
	d.RootFolderID = strings.TrimSpace(d.RootFolderID)
	if d.Cookie == "" {
		return fmt.Errorf("cookie is empty")
	}
	if d.RootFolderID == "" {
		return fmt.Errorf("root folder id is empty")
	}
	d.client = newClient()
	if _, err := d.list(ctx, d.RootFolderID); err != nil {
		return err
	}
	return nil
}

func (d *AliDoc) Drop(ctx context.Context) error {
	d.client = nil
	return nil
}

func (d *AliDoc) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	items, err := d.list(ctx, dir.GetID())
	if err != nil {
		return nil, err
	}

	objs := make([]model.Obj, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.DentryUUID) == "" || strings.TrimSpace(item.Name) == "" {
			continue
		}
		objs = append(objs, toObj(item))
	}
	return objs, nil
}

func (d *AliDoc) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if file == nil || file.IsDir() {
		return nil, fmt.Errorf("alidoc does not support directory links")
	}
	resp, err := d.download(ctx, file.GetID())
	if err != nil {
		return nil, err
	}
	url, err := firstDownloadURL(resp)
	if err != nil {
		return nil, err
	}
	return &model.Link{
		URL: url,
		Header: http.Header{
			"User-Agent": []string{base.UserAgent},
			"Referer":    []string{apiBase + "/"},
		},
	}, nil
}

func (d *AliDoc) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	dirName = strings.TrimSpace(dirName)
	if dirName == "" {
		return nil, fmt.Errorf("folder name is empty")
	}

	parentID := d.RootFolderID
	if parentDir != nil {
		if id := strings.TrimSpace(parentDir.GetID()); id != "" {
			parentID = id
		}
	}

	var result apiResp
	resp, err := d.request(ctx).
		SetBody(map[string]string{
			"dentryType":             "folder",
			"name":                   dirName,
			"parentDentryUuid":       parentID,
			"conflictHandleStrategy": "auto_rename",
		}).
		SetResult(&result).
		SetError(&result).
		Post(apiBase + "/box/api/v2/dentry/createfolder")
	if err != nil {
		return nil, err
	}
	if err := checkResp(resp, result); err != nil {
		return nil, err
	}
	return &Object{
		Object: model.Object{
			Name:     dirName,
			IsFolder: true,
		},
		DentryType: "folder",
	}, nil
}

func (d *AliDoc) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if srcObj == nil {
		return nil, fmt.Errorf("source object is nil")
	}
	if dstDir == nil {
		return nil, fmt.Errorf("destination directory is nil")
	}
	sourceID := strings.TrimSpace(srcObj.GetID())
	targetID := strings.TrimSpace(dstDir.GetID())
	if sourceID == "" {
		return nil, fmt.Errorf("source dentry uuid is empty")
	}
	if targetID == "" {
		return nil, fmt.Errorf("target parent dentry uuid is empty")
	}

	var result apiResp
	resp, err := d.request(ctx).
		SetBody(map[string]interface{}{
			"targetParentDentryUuid": targetID,
			"sourceDentryUuid":       sourceID,
			"operateFrom":            1,
		}).
		SetResult(&result).
		SetError(&result).
		Post(apiBase + "/box/api/v2/dentry/move")
	if err != nil {
		return nil, err
	}
	if err := checkResp(resp, result); err != nil {
		return nil, err
	}
	return srcObj, nil
}

func (d *AliDoc) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	if srcObj == nil {
		return nil, fmt.Errorf("source object is nil")
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return nil, fmt.Errorf("new name is empty")
	}
	dentryUUID := strings.TrimSpace(srcObj.GetID())
	if dentryUUID == "" {
		return nil, fmt.Errorf("dentry uuid is empty")
	}

	var result apiResp
	resp, err := d.request(ctx).
		SetBody(map[string]string{
			"dentryUuid": dentryUUID,
			"name":       newName,
		}).
		SetResult(&result).
		SetError(&result).
		Post(apiBase + "/box/api/v2/dentry/rename")
	if err != nil {
		return nil, err
	}
	if err := checkResp(resp, result); err != nil {
		return nil, err
	}
	return &Object{
		Object: model.Object{
			ID:       srcObj.GetID(),
			Name:     newName,
			Size:     srcObj.GetSize(),
			Modified: srcObj.ModTime(),
			Ctime:    srcObj.CreateTime(),
			IsFolder: srcObj.IsDir(),
			HashInfo: srcObj.GetHash(),
		},
		DentryType: pickAliDocDentryType(srcObj),
	}, nil
}

func (d *AliDoc) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if srcObj == nil {
		return nil, fmt.Errorf("source object is nil")
	}
	if dstDir == nil {
		return nil, fmt.Errorf("destination directory is nil")
	}
	sourceID := strings.TrimSpace(srcObj.GetID())
	targetID := strings.TrimSpace(dstDir.GetID())
	if sourceID == "" {
		return nil, fmt.Errorf("source dentry uuid is empty")
	}
	if targetID == "" {
		return nil, fmt.Errorf("target parent dentry uuid is empty")
	}

	var result apiResp
	resp, err := d.request(ctx).
		SetBody(map[string]interface{}{
			"sourceDentryUuid":       sourceID,
			"targetParentDentryUuid": targetID,
			"operateFrom":            1,
			"onlyCopyMeta":           false,
		}).
		SetResult(&result).
		SetError(&result).
		Post(apiBase + "/box/api/v2/dentry/copy")
	if err != nil {
		return nil, err
	}
	if err := checkResp(resp, result); err != nil {
		return nil, err
	}

	return &Object{
		Object: model.Object{
			Name:     srcObj.GetName(),
			Size:     srcObj.GetSize(),
			Modified: srcObj.ModTime(),
			Ctime:    srcObj.CreateTime(),
			IsFolder: srcObj.IsDir(),
			HashInfo: srcObj.GetHash(),
		},
		DentryType: pickAliDocDentryType(srcObj),
	}, nil
}

func (d *AliDoc) Remove(ctx context.Context, obj model.Obj) error {
	if obj == nil {
		return fmt.Errorf("object is nil")
	}
	dentryUUID := strings.TrimSpace(obj.GetID())
	if dentryUUID == "" {
		return fmt.Errorf("dentry uuid is empty")
	}

	var result apiResp
	resp, err := d.request(ctx).
		SetBody(map[string]string{
			"dentryUuid": dentryUUID,
		}).
		SetResult(&result).
		SetError(&result).
		Post(apiBase + "/box/api/v1/dentry/recycle")
	if err != nil {
		return err
	}
	return checkResp(resp, result)
}

func (d *AliDoc) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	return d.put(ctx, dstDir, file, up)
}

var (
	_ driver.Driver       = (*AliDoc)(nil)
	_ driver.MkdirResult  = (*AliDoc)(nil)
	_ driver.MoveResult   = (*AliDoc)(nil)
	_ driver.RenameResult = (*AliDoc)(nil)
	_ driver.CopyResult   = (*AliDoc)(nil)
	_ driver.Remove       = (*AliDoc)(nil)
	_ driver.PutResult    = (*AliDoc)(nil)
)
