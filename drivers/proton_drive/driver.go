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
	"net/http"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
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
	credentials *common.ProtonDriveCredential

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

	credentialCacheFile string

	// userKR   *crypto.KeyRing
	addrKRs  map[string]*crypto.KeyRing
	addrData map[string]proton.Address

	MainShare *proton.Share
	RootLink  *proton.Link

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
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("ProtonDrive initialization panic: %v", r)
		}
	}()

	if d.Username == "" {
		return fmt.Errorf("username is required")
	}
	if d.Password == "" {
		return fmt.Errorf("password is required")
	}

	// fmt.Printf("ProtonDrive Init: Username=%s, TwoFACode=%s", d.Username, d.TwoFACode)

	if ctx == nil {
		return fmt.Errorf("context cannot be nil")
	}

	cachedCredentials, err := d.loadCachedCredentials()
	useReusableLogin := false
	var reusableCredential *common.ReusableCredentialData

	if err == nil && cachedCredentials != nil &&
		cachedCredentials.UID != "" && cachedCredentials.AccessToken != "" &&
		cachedCredentials.RefreshToken != "" && cachedCredentials.SaltedKeyPass != "" {
		useReusableLogin = true
		reusableCredential = cachedCredentials
	} else {
		useReusableLogin = false
		reusableCredential = &common.ReusableCredentialData{}
	}

	config := &common.Config{
		AppVersion: d.appVersion,
		UserAgent:  d.userAgent,
		FirstLoginCredential: &common.FirstLoginCredentialData{
			Username: d.Username,
			Password: d.Password,
			TwoFA:    d.TwoFACode,
		},
		EnableCaching:              true,
		ConcurrentBlockUploadCount: 5,
		ConcurrentFileCryptoCount:  2,
		UseReusableLogin:           false,
		ReplaceExistingDraft:       true,
		ReusableCredential:         reusableCredential,
		CredentialCacheFile:        d.credentialCacheFile,
	}

	if config.FirstLoginCredential == nil {
		return fmt.Errorf("failed to create login credentials, FirstLoginCredential cannot be nil")
	}

	// fmt.Printf("Calling NewProtonDrive...")

	protonDrive, credentials, err := proton_api_bridge.NewProtonDrive(
		ctx,
		config,
		func(auth proton.Auth) {},
		func() {},
	)

	if credentials == nil && !useReusableLogin {
		return fmt.Errorf("failed to get credentials from NewProtonDrive")
	}

	if err != nil {
		return fmt.Errorf("failed to initialize ProtonDrive: %w", err)
	}

	d.protonDrive = protonDrive

	var finalCredentials *common.ProtonDriveCredential

	if useReusableLogin {

		// For reusable login, create credentials from cached data
		finalCredentials = &common.ProtonDriveCredential{
			UID:           reusableCredential.UID,
			AccessToken:   reusableCredential.AccessToken,
			RefreshToken:  reusableCredential.RefreshToken,
			SaltedKeyPass: reusableCredential.SaltedKeyPass,
		}

		d.credentials = finalCredentials
	} else {
		d.credentials = credentials
	}

	clientOptions := []proton.Option{
		proton.WithAppVersion(d.appVersion),
		proton.WithUserAgent(d.userAgent),
	}
	manager := proton.New(clientOptions...)
	d.c = manager.NewClient(d.credentials.UID, d.credentials.AccessToken, d.credentials.RefreshToken)

	saltedKeyPassBytes, err := base64.StdEncoding.DecodeString(d.credentials.SaltedKeyPass)
	if err != nil {
		return fmt.Errorf("failed to decode salted key pass: %w", err)
	}

	_, addrKRs, addrs, _, err := getAccountKRs(ctx, d.c, nil, saltedKeyPassBytes)
	if err != nil {
		return fmt.Errorf("failed to get account keyrings: %w", err)
	}

	d.MainShare = protonDrive.MainShare
	d.RootLink = protonDrive.RootLink
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
	var linkID string

	if dir.GetPath() == "/" {
		linkID = d.protonDrive.RootLink.LinkID
	} else {

		link, err := d.searchByPath(ctx, dir.GetPath(), true)
		if err != nil {
			return nil, err
		}
		linkID = link.LinkID
	}

	entries, err := d.protonDrive.ListDirectory(ctx, linkID)
	if err != nil {
		return nil, fmt.Errorf("failed to list directory: %w", err)
	}

	// fmt.Printf("Found %d entries for path %s\n", len(entries), dir.GetPath())
	// fmt.Printf("Found %d entries\n", len(entries))

	if len(entries) == 0 {
		emptySlice := []model.Obj{}

		// fmt.Printf("Returning empty slice (entries): %+v\n", emptySlice)

		return emptySlice, nil
	}

	var objects []model.Obj
	for _, entry := range entries {
		obj := &model.Object{
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
	link, err := d.searchByPath(ctx, file.GetPath(), false)
	if err != nil {
		return nil, err
	}

	if err := d.ensureTempServer(); err != nil {
		return nil, fmt.Errorf("failed to start temp server: %w", err)
	}

	token := d.generateDownloadToken(link.LinkID, file.GetName())

	/* return &model.Link{
		URL: fmt.Sprintf("protondrive://download/%s", link.LinkID),
	}, nil */

	//fmt.Printf("d.TempServerPublicHost: %v\n", d.TempServerPublicHost)
	//fmt.Printf("d.TempServerPublicPort: %d\n", d.TempServerPublicPort)

	// Use public host and port for the URL returned to clients
	return &model.Link{
		URL: fmt.Sprintf("http://%s:%d/temp/%s",
			d.TempServerPublicHost, d.TempServerPublicPort, token),
	}, nil
}

//Causes 500 error, but leave it because is an alternative
/* func (d *ProtonDrive) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	link, err := d.searchByPath(ctx, file.GetPath(), false)
	if err != nil {
		return nil, err
	}
	token := d.generateDownloadToken(link.LinkID, file.GetName())
	size := file.GetSize()

	rangeReaderFunc := func(rangeCtx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
		length := httpRange.Length
		if length < 0 || httpRange.Start+length > size {
			length = size - httpRange.Start
		}
		d.tokenMutex.RLock()
		info, exists := d.downloadTokens[token]
		d.tokenMutex.RUnlock()
		if !exists {
			return nil, errors.New("invalid or expired token")
		}
		link, err := d.protonDrive.GetLink(rangeCtx, info.LinkID)
		if err != nil {
			return nil, fmt.Errorf("failed get file link: %+v", err)
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

	return &model.Link{
		RangeReader: &model.FileRangeReader{
			RangeReaderIF: stream.RateLimitRangeReaderFunc(rangeReaderFunc),
		},
	}, nil
} */

func (d *ProtonDrive) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	var parentLinkID string

	if parentDir.GetPath() == "/" {
		parentLinkID = d.protonDrive.RootLink.LinkID
	} else {
		link, err := d.searchByPath(ctx, parentDir.GetPath(), true)
		if err != nil {
			return nil, err
		}
		parentLinkID = link.LinkID
	}

	_, err := d.protonDrive.CreateNewFolderByID(ctx, parentLinkID, dirName)
	if err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	newDir := &model.Object{
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
	link, err := d.searchByPath(ctx, obj.GetPath(), obj.IsDir())
	if err != nil {
		return err
	}

	if obj.IsDir() {
		return d.protonDrive.MoveFolderToTrashByID(ctx, link.LinkID, false)
	} else {
		return d.protonDrive.MoveFileToTrashByID(ctx, link.LinkID)
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
