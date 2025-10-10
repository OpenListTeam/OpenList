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
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
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

	tempServer     *http.Server
	downloadTokens map[string]*downloadInfo
	tokenMutex     sync.RWMutex

	c *proton.Client
	// m *proton.Manager

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

func (d *ProtonDrive) Init(ctx context.Context) error {

	if d.Email == "" {
		return fmt.Errorf("email is required")
	}
	if d.Password == "" {
		return fmt.Errorf("password is required")
	}

	useReusableLogin := false
	reusableCredential := &d.ReusableCredential

	if d.UseReusableLogin && reusableCredential.UID != "" && reusableCredential.AccessToken != "" &&
		reusableCredential.RefreshToken != "" && reusableCredential.SaltedKeyPass != "" {
		useReusableLogin = true
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
		ConcurrentBlockUploadCount: 5,
		ConcurrentFileCryptoCount:  2,
		UseReusableLogin:           useReusableLogin,
		ReplaceExistingDraft:       true,
		ReusableCredential:         reusableCredential,
	}

	protonDrive, _, err := proton_api_bridge.NewProtonDrive(
		ctx,
		config,
		func(auth proton.Auth) {},
		func() {},
	)

	if err != nil {
		return fmt.Errorf("failed to initialize ProtonDrive: %w", err)
	}

	clientOptions := []proton.Option{
		proton.WithAppVersion(d.appVersion),
		proton.WithUserAgent(d.userAgent),
	}
	manager := proton.New(clientOptions...)
	d.c = manager.NewClient(d.ReusableCredential.UID, d.ReusableCredential.AccessToken, d.ReusableCredential.RefreshToken)

	saltedKeyPassBytes, err := base64.StdEncoding.DecodeString(d.ReusableCredential.SaltedKeyPass)
	if err != nil {
		return fmt.Errorf("failed to decode salted key pass: %w", err)
	}

	_, addrKRs, addrs, _, err := getAccountKRs(ctx, d.c, nil, saltedKeyPassBytes)
	if err != nil {
		return fmt.Errorf("failed to get account keyrings: %w", err)
	}

	d.protonDrive = protonDrive
	d.MainShare = protonDrive.MainShare
	if d.RootFolderID == "root" || d.RootFolderID == "" {
		d.RootFolderID = protonDrive.RootLink.LinkID
	}
	d.MainShareKR = protonDrive.MainShareKR
	d.DefaultAddrKR = protonDrive.DefaultAddrKR
	d.addrKRs = addrKRs
	d.addrData = addrs

	return nil
}

func (d *ProtonDrive) Drop(ctx context.Context) error {
	if d.tempServer != nil {
		d.tempServer.Shutdown(ctx)
	}
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
	link, err := d.protonDrive.GetLink(ctx, file.GetID())
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
		RangeReader: &model.FileRangeReader{
			RangeReaderIF: stream.RateLimitRangeReaderFunc(rangeReaderFunc),
		},
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

	srcLink, err := d.searchByPath(ctx, srcObj.GetPath(), false)
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

	tempFile, err := utils.CreateTempFile(reader, actualSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer tempFile.Close()

	updatedObj := &model.Object{
		Name: srcObj.GetName(),
		// Use the accurate and real size
		Size:     actualSize,
		Modified: srcObj.ModTime(),
		IsFolder: false,
	}

	return d.Put(ctx, dstDir, &stream.FileStream{
		Ctx:    ctx,
		Obj:    updatedObj,
		Reader: tempFile,
	}, nil)
}

func (d *ProtonDrive) Remove(ctx context.Context, obj model.Obj) error {
	if obj.IsDir() {
		return d.protonDrive.MoveFolderToTrashByID(ctx, obj.GetID(), false)
	} else {
		return d.protonDrive.MoveFileToTrashByID(ctx, obj.GetID())
	}
}

func (d *ProtonDrive) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	var parentLinkID string

	if dstDir.GetPath() == "/" {
		parentLinkID = d.protonDrive.RootLink.LinkID
	} else {
		link, err := d.searchByPath(ctx, dstDir.GetPath(), true)
		if err != nil {
			return nil, err
		}
		parentLinkID = link.LinkID
	}

	err := d.uploadFile(ctx, parentLinkID, file, up)
	if err != nil {
		return nil, err
	}

	uploadedObj := &model.Object{
		Name:     file.GetName(),
		Size:     file.GetSize(),
		Modified: file.ModTime(),
		IsFolder: false,
	}
	return uploadedObj, nil
}

func (d *ProtonDrive) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	about, err := d.protonDrive.About(ctx)
	if err != nil {
		return nil, err
	}
	total := uint64(about.MaxSpace)
	free := total - uint64(about.UsedSpace)
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			FreeSpace:  free,
		},
	}, nil
}

var _ driver.Driver = (*ProtonDrive)(nil)
