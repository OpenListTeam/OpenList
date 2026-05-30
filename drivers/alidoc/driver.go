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
	parentID := d.RootFolderID
	parentPath := "/"
	if dir != nil {
		if id := strings.TrimSpace(dir.GetID()); id != "" {
			parentID = id
		}
		if p := dir.GetPath(); p != "" {
			parentPath = p
		}
	}

	items, err := d.list(ctx, parentID)
	if err != nil {
		return nil, err
	}

	objs := make([]model.Obj, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.DentryUUID) == "" || strings.TrimSpace(item.Name) == "" {
			continue
		}
		objs = append(objs, toObj(parentPath, item))
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
	return nil, readonlyError()
}

func (d *AliDoc) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return nil, readonlyError()
}

func (d *AliDoc) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	return nil, readonlyError()
}

func (d *AliDoc) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return nil, readonlyError()
}

func (d *AliDoc) Remove(ctx context.Context, obj model.Obj) error {
	return readonlyError()
}

func (d *AliDoc) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	return nil, readonlyError()
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
