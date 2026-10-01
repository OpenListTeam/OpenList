package filen

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/pquerna/otp/totp"

	sdk "github.com/FilenCloudDienste/filen-sdk-go/filen"
	filentypes "github.com/FilenCloudDienste/filen-sdk-go/filen/types"
)

type Filen struct {
	model.Storage
	Addition

	client *sdk.Filen
}

func (d *Filen) Config() driver.Config {
	return config
}

func (d *Filen) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Filen) Init(ctx context.Context) error {
	var client *sdk.Filen
	var err error
	if d.APIKey != "" {
		// 已配置 API key，直接使用，避免每次挂载都触发登录
		client, err = sdk.NewWithAPIKey(ctx, d.Email, d.Password, d.APIKey)
	} else {
		// 未配置 API key，用邮箱密码登录获取；成功后回填到配置并持久化，后续挂载直接复用
		client, err = sdk.New(ctx, d.Email, d.Password, d.twoFACode())
		if err == nil {
			d.APIKey = client.Client.APIKey
			op.MustSaveDriverStorage(d)
		}
	}
	if err != nil {
		return fmt.Errorf("failed to initialize filen client: %w", err)
	}
	d.client = client
	// 若未显式配置根目录 ID，则使用账号的根目录 UUID
	if d.RootFolderID == "" {
		d.RootFolderID = client.BaseFolder.GetUUID()
	}
	return nil
}

// twoFACode 返回登录用的两步验证码。
// 配置了 2FA 密钥时用 TOTP 算法生成动态码；否则传占位符 "XXXXXX"
// （空字符串会被服务端拒绝 invalid_params）。
func (d *Filen) twoFACode() string {
	if d.TwoFASecret == "" {
		return "XXXXXX"
	}
	code, err := totp.GenerateCode(d.TwoFASecret, time.Now())
	if err != nil {
		// 密钥格式非法时退回占位符，让服务端报出明确的 2FA 错误
		return "XXXXXX"
	}
	return code
}

func (d *Filen) Drop(ctx context.Context) error {
	d.client = nil
	return nil
}

// rootDir 返回当前配置的根目录。
func (d *Filen) rootDir() filentypes.DirectoryInterface {
	if d.RootFolderID == "" || d.RootFolderID == d.client.BaseFolder.GetUUID() {
		return &d.client.BaseFolder
	}
	return filentypes.NewRootDirectory(d.RootFolderID)
}

// toObj 将 SDK 的 File/Directory 转换为 model.Obj。
// parentPath 为父目录路径，用于拼接子对象的完整路径。
func fileToObj(f *filentypes.File, parentPath string) model.Obj {
	return &model.Object{
		ID:       f.UUID,
		Path:     joinPath(parentPath, f.Name),
		Name:     f.Name,
		Size:     f.Size,
		Modified: f.LastModified,
		Ctime:    f.Created,
		IsFolder: false,
	}
}

func dirToObj(dir *filentypes.Directory, parentPath string) model.Obj {
	return &model.Object{
		ID:       dir.UUID,
		Path:     joinPath(parentPath, dir.Name),
		Name:     dir.Name,
		Size:     0,
		Modified: dir.Created,
		Ctime:    dir.Created,
		IsFolder: true,
	}
}

// joinPath 拼接路径，处理父路径为根的情况。
func joinPath(parent, name string) string {
	if parent == "" || parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

// Get 实现 driver.Getter 接口，按路径直接获取对象，避免全量 List。
// 返回的 Obj 会携带 Path，供后续 List/Link/Move/Remove 使用。
func (d *Filen) Get(ctx context.Context, path string) (model.Obj, error) {
	item, err := d.client.FindItem(ctx, path)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, errs.ObjectNotFound
	}
	switch v := item.(type) {
	case *filentypes.File:
		return fileToObj(v, parentOf(path)), nil
	case *filentypes.Directory:
		return dirToObj(v, parentOf(path)), nil
	case *filentypes.RootDirectory:
		// 根目录
		return &model.Object{
			ID:       v.UUID,
			Path:     "/",
			Name:     "root",
			IsFolder: true,
		}, nil
	}
	return nil, errs.ObjectNotFound
}

// parentOf 返回路径的父目录。
func parentOf(path string) string {
	if path == "" || path == "/" {
		return ""
	}
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return ""
}

func (d *Filen) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	parentPath := dir.GetPath()
	files, dirs, err := d.client.ReadDirectory(ctx, filentypes.NewRootDirectory(dir.GetID()))
	if err != nil {
		return nil, err
	}
	objs := make([]model.Obj, 0, len(files)+len(dirs))
	for _, dir := range dirs {
		objs = append(objs, dirToObj(dir, parentPath))
	}
	for _, file := range files {
		objs = append(objs, fileToObj(file, parentPath))
	}
	return objs, nil
}

func (d *Filen) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	// 通过路径找到文件以获取解密所需的完整元数据
	f, err := d.client.FindFile(ctx, file.GetPath())
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errs.ObjectNotFound
	}

	size := f.Size
	rangeReaderFunc := func(rangeCtx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
		length := httpRange.Length
		if length < 0 || httpRange.Start+length > size {
			length = size - httpRange.Start
		}
		reader := d.client.GetDownloadReaderWithOffset(rangeCtx, f, httpRange.Start, httpRange.Start+length)
		return reader, nil
	}

	return &model.Link{
		RangeReader:   stream.RateLimitRangeReaderFunc(rangeReaderFunc),
		ContentLength: size,
	}, nil
}

func (d *Filen) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	dir, err := d.client.CreateDirectoryWithParentUUID(ctx, parentDir.GetID(), dirName)
	if err != nil {
		return nil, err
	}
	return dirToObj(dir, parentDir.GetPath()), nil
}

func (d *Filen) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	item, err := d.findItemByPath(ctx, srcObj)
	if err != nil {
		return nil, err
	}
	if err := d.client.MoveItem(ctx, item, dstDir.GetID(), true); err != nil {
		return nil, err
	}
	return srcObj, nil
}

func (d *Filen) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	item, err := d.findItemByPath(ctx, srcObj)
	if err != nil {
		return nil, err
	}
	if err := d.client.Rename(ctx, item, newName); err != nil {
		return nil, err
	}
	return srcObj, nil
}

func (d *Filen) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	// Filen API 不支持服务端直接复制
	return nil, errs.NotImplement
}

func (d *Filen) Remove(ctx context.Context, obj model.Obj) error {
	if obj.IsDir() {
		dir, err := d.client.FindDirectory(ctx, obj.GetPath())
		if err != nil {
			return err
		}
		if dir == nil {
			return errs.ObjectNotFound
		}
		return d.client.TrashDirectory(ctx, dir)
	}
	f, err := d.client.FindFile(ctx, obj.GetPath())
	if err != nil {
		return err
	}
	if f == nil {
		return errs.ObjectNotFound
	}
	return d.client.TrashFile(ctx, *f)
}

func (d *Filen) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	// 缓存上传流到临时文件并回报进度
	tmpF, err := file.CacheFullAndWriter(&up, nil)
	if err != nil {
		return nil, err
	}

	parent := filentypes.NewRootDirectory(dstDir.GetID())
	incomplete, err := filentypes.NewIncompleteFile(
		d.client.FileEncryptionVersion,
		file.GetName(),
		file.GetMimetype(),
		file.ModTime(),
		file.ModTime(),
		parent,
	)
	if err != nil {
		return nil, err
	}

	reader := driver.NewLimitedUploadStream(ctx, tmpF)
	uploaded, err := d.client.UploadFile(ctx, incomplete, reader)
	if err != nil {
		return nil, err
	}
	return fileToObj(uploaded, dstDir.GetPath()), nil
}

func (d *Filen) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	info, err := d.client.GetUserInfo(ctx)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: info.MaxStorage,
			UsedSpace:  info.UsedStorage,
		},
	}, nil
}

// findItemByPath 根据 model.Obj 的路径构造 SDK 的文件系统对象。
func (d *Filen) findItemByPath(ctx context.Context, obj model.Obj) (filentypes.NonRootFileSystemObject, error) {
	path := obj.GetPath()
	if obj.IsDir() {
		dir, err := d.client.FindDirectory(ctx, path)
		if err != nil {
			return nil, err
		}
		if dir == nil {
			return nil, errs.ObjectNotFound
		}
		switch v := dir.(type) {
		case *filentypes.Directory:
			return v, nil
		case *filentypes.RootDirectory:
			// 根目录不能作为 NonRootFileSystemObject，但 Move/Rename 不会对根目录操作
			return nil, fmt.Errorf("cannot operate on root directory")
		}
		return nil, errs.ObjectNotFound
	}
	f, err := d.client.FindFile(ctx, path)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errs.ObjectNotFound
	}
	return f, nil
}

var _ driver.Driver = (*Filen)(nil)
