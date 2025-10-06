package micloud

import (
	"context"
	"fmt"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type MiCloud struct {
	model.Storage
	Addition
	client *MiCloudClient // 小米云盘客户端
}

func (d *MiCloud) Config() driver.Config {
	return config
}

func (d *MiCloud) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *MiCloud) Init(ctx context.Context) error {
	// 初始化小米云盘客户端
	client, err := NewMiCloudClient(d.UserId, d.ServiceToken, d.DeviceId)
	if err != nil {
		return err
	}
	d.client = client
	// 当 cookie（含 serviceToken）刷新时，写回 Addition 并持久化
	d.client.SetOnCookieUpdate(func(userId, serviceToken, deviceId string) {
		// 更新 Addition
		d.UserId = userId
		d.ServiceToken = serviceToken
		d.DeviceId = deviceId
		// 持久化到数据库
		op.MustSaveDriverStorage(d)
	})

	// 登录或刷新token
	if err := d.client.Login(); err != nil {
		return fmt.Errorf("failed to login to MiCloud: %w", err)
	}
	// 启动后台自动续期 serviceToken
	d.client.StartAutoRenewal()

	return nil
}

func (d *MiCloud) Drop(ctx context.Context) error {
	// 停止自动续期
	if d.client != nil {
		d.client.StopAutoRenewal()
	}
	return nil
}

func (d *MiCloud) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if d.client == nil {
		return nil, fmt.Errorf("MiCloud client not initialized")
	}

	// 获取目录ID
	folderId := d.client.rootId
	if dir.GetID() != "" {
		folderId = dir.GetID()
	}

	// 获取目录列表
	files, err := d.client.GetFolder(folderId)
	if err != nil {
		return nil, err
	}

	var objects []model.Obj
	for _, file := range files {
		obj := ConvertFileToObj(file)
		objects = append(objects, obj)
	}

	return objects, nil
}

func (d *MiCloud) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if d.client == nil {
		return nil, fmt.Errorf("MiCloud client not initialized")
	}
	// 直接获取直链
	url, err := d.client.GetFileDownLoadUrl(file.GetID())
	if err != nil {
		return nil, err
	}
	return &model.Link{URL: url}, nil
}

func (d *MiCloud) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	if d.client == nil {
		return nil, fmt.Errorf("MiCloud client not initialized")
	}

	// 创建目录
	folderId := d.client.rootId
	if parentDir.GetID() != "" {
		folderId = parentDir.GetID()
	}

	newDirId, err := d.client.CreateFolder(dirName, folderId)
	if err != nil {
		return nil, err
	}

	// 创建目录对象
	obj := &model.Object{
		ID:       newDirId,
		Name:     dirName,
		Size:     0,
		Modified: time.Now(),
		IsFolder: true,
	}

	return obj, nil
}

func (d *MiCloud) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if d.client == nil {
		return nil, fmt.Errorf("MiCloud client not initialized")
	}

	// 移动文件/目录
	movedObj, err := d.client.Move(srcObj.GetID(), dstDir.GetID())
	if err != nil {
		return nil, err
	}

	obj := ConvertFileToObj(*movedObj)

	return obj, nil
}

func (d *MiCloud) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	if d.client == nil {
		return nil, fmt.Errorf("MiCloud client not initialized")
	}

	// 重命名文件/目录
	renamedObj, err := d.client.Rename(srcObj.GetID(), newName)
	if err != nil {
		return nil, err
	}

	obj := ConvertFileToObj(*renamedObj)

	return obj, nil
}

func (d *MiCloud) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return nil, errs.NotSupport
}

func (d *MiCloud) Remove(ctx context.Context, obj model.Obj) error {
	if d.client == nil {
		return fmt.Errorf("MiCloud client not initialized")
	}

	// 删除文件/目录
	return d.client.Delete(obj.GetID())
}

func (d *MiCloud) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	if d.client == nil {
		return nil, fmt.Errorf("MiCloud client not initialized")
	}

	// 上传文件
	uploadedObj, err := d.client.Upload(dstDir.GetID(), file, up)
	if err != nil {
		return nil, err
	}

	obj := ConvertFileToObj(*uploadedObj)

	return obj, nil
}

func (d *MiCloud) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	return nil, errs.NotImplement
}

func (d *MiCloud) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *MiCloud) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	return nil, errs.NotImplement
}

func (d *MiCloud) ArchiveDecompress(ctx context.Context, srcObj, dstDir model.Obj, args model.ArchiveDecompressArgs) ([]model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *MiCloud) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	if d.client == nil {
		return nil, fmt.Errorf("MiCloud client not initialized")
	}

	// 获取存储详情
	details, err := d.client.GetStorageDetails()
	if err != nil {
		return nil, err
	}

	return details, nil
}

//func (d *MiCloud) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
//	return nil, errs.NotSupport
//}

var _ driver.Driver = (*MiCloud)(nil)
