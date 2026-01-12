package protondrive

/*
Package protondrive
Author: Da3zKi7<da3zki7@duck.com>
Date: 2025-09-18

Thanks to @henrybear327 for modded go-proton-api & Proton-API-Bridge

The power of open-source, the force of teamwork and the magic of reverse engineering!


D@' 3z K!7 - The King Of Cracking

Да здравствует Родина))
*/

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/ProtonMail/gopenpgp/v2/crypto"
	proton_api_bridge "github.com/henrybear327/Proton-API-Bridge"
	"github.com/henrybear327/Proton-API-Bridge/common"
	"github.com/henrybear327/go-proton-api"
)

type ProtonDrive struct {
	model.Storage
	Addition

	protonDrive *proton_api_bridge.ProtonDrive

	apiBase    string
	appVersion string
	protonJson string
	userAgent  string
	sdkVersion string
	webDriveAV string

	c *proton.Client

	// userKR   *crypto.KeyRing
	addrKRs  map[string]*crypto.KeyRing
	addrData map[string]proton.Address

	MainShare *proton.Share

	DefaultAddrKR *crypto.KeyRing
	MainShareKR   *crypto.KeyRing
}

func (d *ProtonDrive) Config() driver.Config {
	return config
}

func (d *ProtonDrive) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *ProtonDrive) Init(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); err == nil && r != nil {
			err = fmt.Errorf("ProtonDrive initialization panic: %v", r)
		}
	}()

	if d.Email == "" {
		return fmt.Errorf("email is required")
	}
	if d.Password == "" {
		return fmt.Errorf("password is required")
	}

	config := &common.Config{
		AppVersion: d.appVersion,
		UserAgent:  d.userAgent,
		FirstLoginCredential: &common.FirstLoginCredentialData{
			Username: d.Email,
			Password: d.Password,
			TwoFA:    d.TwoFACode,
		},
		EnableCaching:              true,
		ConcurrentBlockUploadCount: setting.GetInt(conf.TaskUploadThreadsNum, conf.Conf.Tasks.Upload.Workers),
		//ConcurrentFileCryptoCount:  2,
		UseReusableLogin:     d.UseReusableLogin && d.ReusableCredential != (common.ReusableCredentialData{}),
		ReplaceExistingDraft: true,
		ReusableCredential:   &d.ReusableCredential,
	}

	protonDrive, _, err := proton_api_bridge.NewProtonDrive(
		ctx,
		config,
		d.authHandler,
		func() {},
	)

	if err != nil && config.UseReusableLogin {
		config.UseReusableLogin = false
		protonDrive, _, err = proton_api_bridge.NewProtonDrive(ctx,
			config,
			d.authHandler,
			func() {},
		)
		if err == nil {
			op.MustSaveDriverStorage(d)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to initialize ProtonDrive: %w", err)
	}

	if err := d.initClient(ctx); err != nil {
		return err
	}

	d.protonDrive = protonDrive
	d.MainShare = protonDrive.MainShare
	if d.RootFolderID == "root" || d.RootFolderID == "" {
		d.RootFolderID = protonDrive.RootLink.LinkID
	}
	d.MainShareKR = protonDrive.MainShareKR
	d.DefaultAddrKR = protonDrive.DefaultAddrKR

	return nil
}

func (d *ProtonDrive) Drop(ctx context.Context) error {
	return nil
}

func (d *ProtonDrive) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	entries, err := d.protonDrive.ListDirectory(ctx, dir.GetID())
	if err != nil {
		return nil, fmt.Errorf("failed to list directory: %w", err)
	}

	objects := make([]model.Obj, 0, len(entries))
	for _, entry := range entries {
		obj := &model.Object{
			ID:       entry.Link.LinkID,
			Name:     entry.Name,
			Size:     entry.Link.Size,
			Modified: time.Unix(entry.Link.ModifyTime, 0),
			IsFolder: entry.IsFolder,
		}
		objects = append(objects, obj)
	}

	return objects, nil
}

func (d *ProtonDrive) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	link, err := d.getLink(ctx, file.GetID())
	if err != nil {
		return nil, fmt.Errorf("failed get file link: %+v", err)
	}
	fileSystemAttrs, err := d.protonDrive.GetActiveRevisionAttrs(ctx, link)
	if err != nil {
		return nil, fmt.Errorf("failed get file revision: %+v", err)
	}
	// 解密后的文件大小
	size := fileSystemAttrs.Size

	rangeReaderFunc := func(rangeCtx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
		length := httpRange.Length
		if length < 0 || httpRange.Start+length > size {
			length = size - httpRange.Start
		}
		reader, _, _, err := d.protonDrive.DownloadFile(rangeCtx, link, httpRange.Start)
		if err != nil {
			return nil, fmt.Errorf("failed start download: %+v", err)
		}
		return utils.ReadCloser{
			Reader: io.LimitReader(reader, length),
			Closer: reader,
		}, nil
	}

	expiration := time.Minute
	return &model.Link{
		RangeReader:   stream.RateLimitRangeReaderFunc(rangeReaderFunc),
		ContentLength: size,
		Expiration:    &expiration,
	}, nil
}

func (d *ProtonDrive) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	id, err := d.protonDrive.CreateNewFolderByID(ctx, parentDir.GetID(), dirName)
	if err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	newDir := &model.Object{
		ID:       id,
		Name:     dirName,
		IsFolder: true,
		Modified: time.Now(),
	}
	return newDir, nil
}

func (d *ProtonDrive) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return d.DirectMove(ctx, srcObj, dstDir)
}

func (d *ProtonDrive) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	if d.protonDrive == nil {
		return nil, fmt.Errorf("protonDrive bridge is nil")
	}

	return d.DirectRename(ctx, srcObj, newName)
}

func (d *ProtonDrive) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if srcObj.IsDir() {
		return nil, fmt.Errorf("directory copy not supported")
	}

	srcLink, err := d.getLink(ctx, srcObj.GetID())
	if err != nil {
		return nil, err
	}

	reader, linkSize, fileSystemAttrs, err := d.protonDrive.DownloadFile(ctx, srcLink, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to download source file: %w", err)
	}
	defer reader.Close()

	actualSize := linkSize
	if fileSystemAttrs != nil && fileSystemAttrs.Size > 0 {
		actualSize = fileSystemAttrs.Size
	}

	file := &stream.FileStream{
		Ctx: ctx,
		Obj: &model.Object{
			Name: srcObj.GetName(),
			// Use the accurate and real size
			Size:     actualSize,
			Modified: srcObj.ModTime(),
		},
		Reader: reader,
	}
	defer file.Close()
	return d.Put(ctx, dstDir, file, func(percentage float64) {})
}

func (d *ProtonDrive) Remove(ctx context.Context, obj model.Obj) error {
	if obj.IsDir() {
		return d.protonDrive.MoveFolderToTrashByID(ctx, obj.GetID(), false)
	} else {
		return d.protonDrive.MoveFileToTrashByID(ctx, obj.GetID())
	}
}

func (d *ProtonDrive) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	return d.uploadFile(ctx, dstDir.GetID(), file, up)
}

func (d *ProtonDrive) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	about, err := d.protonDrive.About(ctx)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: about.MaxSpace,
			UsedSpace:  about.UsedSpace,
		},
	}, nil
}

var _ driver.Driver = (*ProtonDrive)(nil)
