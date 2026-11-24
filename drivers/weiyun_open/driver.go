package weiyun_open

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type WeiYunOpen struct {
	model.Storage
	Addition

	client *mcpClient
	root   *Folder
}

func (d *WeiYunOpen) Config() driver.Config {
	return config
}

func (d *WeiYunOpen) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *WeiYunOpen) Init(ctx context.Context) error {
	if d.MCPToken == "" {
		return errs.EmptyToken
	}
	if d.RootDirKey != "" && d.RootPDirKey == "" {
		return errors.New("root_pdir_key is required when root_dir_key is set")
	}
	d.client = newMCPClient(d.Addition)
	root, err := d.discoverRoot(ctx)
	if err != nil {
		return err
	}
	d.root = root
	return nil
}

func (d *WeiYunOpen) Drop(ctx context.Context) error {
	d.client = nil
	d.root = nil
	return nil
}

func (d *WeiYunOpen) GetRoot(ctx context.Context) (model.Obj, error) {
	if d.root == nil {
		return nil, errors.New("weiyun open driver is not initialized")
	}
	return d.root, nil
}

func (d *WeiYunOpen) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	folder, ok := dir.(*Folder)
	if !ok {
		return nil, errs.NotSupport
	}
	offset := 0
	objects := make([]model.Obj, 0)
	for {
		page, err := d.listPage(ctx, folder, offset)
		if err != nil {
			return nil, err
		}
		objects = append(objects, d.pageObjects(page)...)
		if page.FinishFlag {
			return objects, nil
		}
		pageCount := len(page.DirList) + len(page.FileList)
		if pageCount == 0 {
			return nil, errors.New("weiyun list returned empty page before finish")
		}
		offset += pageCount
	}
}

func (d *WeiYunOpen) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	target, ok := file.(*File)
	if !ok {
		return nil, errs.NotSupport
	}
	resp := downloadResponse{}
	err := d.client.call(ctx, "weiyun.download", downloadArgs{
		Items: []downloadFileItem{{FileID: target.FileID, PdirKey: target.ParentKey}},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if err = responseError(resp.toolResponse); err != nil {
		return nil, err
	}
	item, err := findDownloadItem(resp.Items, target.FileID)
	if err != nil {
		return nil, err
	}
	return &model.Link{
		URL: item.HTTPSDownloadURL,
		Header: http.Header{
			"Cookie": []string{item.Cookie},
		},
	}, nil
}

func (d *WeiYunOpen) Remove(ctx context.Context, obj model.Obj) error {
	switch target := obj.(type) {
	case *File:
		return d.removeFile(ctx, target)
	case *Folder:
		if target.Root {
			return errs.NotSupport
		}
		return d.removeFolder(ctx, target)
	default:
		return errs.NotSupport
	}
}

func (d *WeiYunOpen) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	folder, ok := parentDir.(*Folder)
	if !ok {
		return nil, errs.NotSupport
	}
	resp := createDirResponse{}
	err := d.client.call(ctx, "weiyun.create_dir", createDirArgs{
		PdirKey: folder.DirKey,
		DirName: dirName,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if err = responseError(resp.toolResponse); err != nil {
		return nil, err
	}
	now := jsonInt64(time.Now().UnixMilli())
	return newFolder(folder.DirKey, dirItem{
		DirKey:   resp.DirKey,
		DirName:  resp.DirName,
		DirCTime: now,
		DirMTime: now,
	}), nil
}

func (d *WeiYunOpen) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	folder, ok := dstDir.(*Folder)
	if !ok {
		return nil, errs.NotSupport
	}
	switch target := srcObj.(type) {
	case *File:
		return d.moveFileResult(ctx, target, folder)
	case *Folder:
		return d.moveFolderResult(ctx, target, folder)
	default:
		return nil, errs.NotSupport
	}
}

func (d *WeiYunOpen) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	switch target := srcObj.(type) {
	case *File:
		return d.renameFileResult(ctx, target, newName)
	case *Folder:
		return d.renameFolderResult(ctx, target, newName)
	default:
		return nil, errs.NotSupport
	}
}

func (d *WeiYunOpen) discoverRoot(ctx context.Context) (*Folder, error) {
	root := &Folder{Root: true, DirKey: d.RootDirKey, ParentKey: d.RootPDirKey, DirName: defaultRootName}
	page, err := d.listPage(ctx, root, 0)
	if err != nil {
		return nil, err
	}
	return newRootFolder(page.PdirKey, d.RootPDirKey), nil
}

func (d *WeiYunOpen) listPage(ctx context.Context, folder *Folder, offset int) (*listResponse, error) {
	resp := listResponse{}
	err := d.client.call(ctx, "weiyun.list", d.newListArgs(folder, offset), &resp)
	if err != nil {
		return nil, err
	}
	if err = responseError(resp.toolResponse); err != nil {
		return nil, err
	}
	if resp.PdirKey == "" {
		return nil, errors.New("weiyun list returned empty pdir_key")
	}
	return &resp, nil
}

func (d *WeiYunOpen) newListArgs(folder *Folder, offset int) listArgs {
	args := listArgs{
		Offset:  uint32(offset),
		Limit:   listPageSize,
		OrderBy: d.orderByCode(),
		Asc:     d.OrderDirection == "asc",
	}
	if folder.Root && d.RootDirKey == "" {
		return args
	}
	args.DirKey = folder.DirKey
	args.PdirKey = folder.ParentKey
	return args
}

func (d *WeiYunOpen) orderByCode() uint32 {
	switch d.OrderBy {
	case "name":
		return orderByName
	case "modified":
		return orderByModified
	default:
		return orderByNone
	}
}

func (d *WeiYunOpen) pageObjects(page *listResponse) []model.Obj {
	// According to weiyun/SKILL.md, all follow-up operations must use the
	// response top-level pdir_key instead of the entry's own pdir_key field.
	objects := make([]model.Obj, 0, len(page.DirList)+len(page.FileList))
	for _, item := range page.DirList {
		objects = append(objects, newFolder(page.PdirKey, item))
	}
	for _, item := range page.FileList {
		objects = append(objects, newFile(page.PdirKey, item))
	}
	return objects
}

func (d *WeiYunOpen) removeFile(ctx context.Context, file *File) error {
	resp := deleteResponse{}
	err := d.client.call(ctx, "weiyun.delete", deleteArgs{
		FileList:         []deleteFileItem{{FileID: file.FileID, PdirKey: file.ParentKey}},
		DeleteCompletely: d.DeleteCompletely,
	}, &resp)
	if err != nil {
		return err
	}
	return responseError(resp.toolResponse)
}

func (d *WeiYunOpen) removeFolder(ctx context.Context, folder *Folder) error {
	resp := deleteResponse{}
	err := d.client.call(ctx, "weiyun.delete", deleteArgs{
		DirList:          []deleteDirItem{{DirKey: folder.DirKey, PdirKey: folder.ParentKey}},
		DeleteCompletely: d.DeleteCompletely,
	}, &resp)
	if err != nil {
		return err
	}
	return responseError(resp.toolResponse)
}

func (d *WeiYunOpen) moveFileResult(ctx context.Context, file *File, dst *Folder) (model.Obj, error) {
	if err := d.moveFile(ctx, file, dst); err != nil {
		return nil, err
	}
	moved := *file
	moved.ParentKey = dst.DirKey
	return &moved, nil
}

func (d *WeiYunOpen) moveFolderResult(ctx context.Context, folder *Folder, dst *Folder) (model.Obj, error) {
	if folder.Root {
		return nil, errs.NotSupport
	}
	if err := d.moveFolder(ctx, folder, dst); err != nil {
		return nil, err
	}
	moved := *folder
	moved.ParentKey = dst.DirKey
	return &moved, nil
}

func (d *WeiYunOpen) renameFileResult(ctx context.Context, file *File, newName string) (model.Obj, error) {
	if err := d.renameFile(ctx, file, newName); err != nil {
		return nil, err
	}
	renamed := *file
	renamed.FileName = newName
	renamed.FileMTime = time.Now().UnixMilli()
	return &renamed, nil
}

func (d *WeiYunOpen) renameFolderResult(ctx context.Context, folder *Folder, newName string) (model.Obj, error) {
	if folder.Root {
		return nil, errs.NotSupport
	}
	if err := d.renameFolder(ctx, folder, newName); err != nil {
		return nil, err
	}
	renamed := *folder
	renamed.DirName = newName
	renamed.DirMTime = time.Now().UnixMilli()
	return &renamed, nil
}

func (d *WeiYunOpen) moveFile(ctx context.Context, file *File, dst *Folder) error {
	resp := toolResponse{}
	err := d.client.call(ctx, "weiyun.move_file", moveFileArgs{
		FileID:     file.FileID,
		SrcPdirKey: file.ParentKey,
		DstPdirKey: dst.DirKey,
		FileName:   file.FileName,
	}, &resp)
	if err != nil {
		return err
	}
	return responseError(resp)
}

func (d *WeiYunOpen) moveFolder(ctx context.Context, folder *Folder, dst *Folder) error {
	resp := toolResponse{}
	err := d.client.call(ctx, "weiyun.move_dir", moveDirArgs{
		DirKey:     folder.DirKey,
		SrcPdirKey: folder.ParentKey,
		DstPdirKey: dst.DirKey,
		DirName:    folder.DirName,
	}, &resp)
	if err != nil {
		return err
	}
	return responseError(resp)
}

func (d *WeiYunOpen) renameFile(ctx context.Context, file *File, newName string) error {
	resp := toolResponse{}
	err := d.client.call(ctx, "weiyun.rename_file", renameFileArgs{
		FileID:      file.FileID,
		PdirKey:     file.ParentKey,
		NewFileName: newName,
	}, &resp)
	if err != nil {
		return err
	}
	return responseError(resp)
}

func (d *WeiYunOpen) renameFolder(ctx context.Context, folder *Folder, newName string) error {
	resp := toolResponse{}
	err := d.client.call(ctx, "weiyun.rename_dir", renameDirArgs{
		DirKey:     folder.DirKey,
		PdirKey:    folder.ParentKey,
		NewDirName: newName,
		SrcDirName: folder.DirName,
	}, &resp)
	if err != nil {
		return err
	}
	return responseError(resp)
}

func responseError(resp toolResponse) error {
	if resp.Error == "" {
		return nil
	}
	return errors.New(resp.Error)
}

func findDownloadItem(items []downloadResultItem, fileID string) (*downloadResultItem, error) {
	for i := range items {
		if items[i].FileID == fileID {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("weiyun download result missing file %s", fileID)
}

var _ driver.Driver = (*WeiYunOpen)(nil)
var _ driver.GetRooter = (*WeiYunOpen)(nil)
var _ driver.MkdirResult = (*WeiYunOpen)(nil)
var _ driver.MoveResult = (*WeiYunOpen)(nil)
var _ driver.Remove = (*WeiYunOpen)(nil)
var _ driver.RenameResult = (*WeiYunOpen)(nil)
