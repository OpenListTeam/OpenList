package halalcloudopen

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func (d *HalalCloudOpen) Drop(ctx context.Context) error {
	return nil
}

func (d *HalalCloudOpen) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return d.getFiles(ctx, dir)
}

func (d *HalalCloudOpen) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return d.getLink(ctx, file, args)
}

func (d *HalalCloudOpen) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	return d.makeDir(ctx, parentDir, dirName)
}

func (d *HalalCloudOpen) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return d.move(ctx, srcObj, dstDir)
}

func (d *HalalCloudOpen) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	return d.rename(ctx, srcObj, newName)
}

func (d *HalalCloudOpen) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return d.copy(ctx, srcObj, dstDir)
}

func (d *HalalCloudOpen) Remove(ctx context.Context, obj model.Obj) error {
	return d.remove(ctx, obj)
}

func (d *HalalCloudOpen) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	return d.put(ctx, dstDir, stream, up)
}

func (d *HalalCloudOpen) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	return d.details(ctx)
}
