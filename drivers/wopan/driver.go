package template

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/wopan-sdk-go"
	"github.com/go-resty/resty/v2"
)

// minAccessTokenLen mirrors the check inside the SDK: the first 16 bytes of the
// access token are used as the AES key of every dispatcher request, so a token
// shorter than that can never be used and must be refreshed first.
const minAccessTokenLen = 16

type Wopan struct {
	model.Storage
	Addition
	client          *wopan.WoClient
	defaultFamilyID string
}

// onTokenRefreshed keeps the driver storage in sync with the tokens issued by
// the SDK, so a refresh survives restarts.
func (d *Wopan) onTokenRefreshed(accessToken, refreshToken string) {
	d.setTokens(accessToken, refreshToken)
	op.MustSaveDriverStorage(d)
}

func (d *Wopan) setTokens(accessToken, refreshToken string) {
	d.AccessToken = accessToken
	d.RefreshToken = refreshToken
}

func (d *Wopan) Config() driver.Config {
	return config
}

func (d *Wopan) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Wopan) Init(ctx context.Context) error {
	d.client = wopan.DefaultWithRefreshToken(d.RefreshToken)
	d.client.SetAccessToken(d.AccessToken)
	d.client.OnRefreshToken(d.onTokenRefreshed)
	return d.initClient(ctx)
}

// initClient prepares the client before any dispatcher request is served.
//
// Two things have to happen here, in this order:
//
//  1. The browser User-Agent must be applied to the underlying resty client.
//     The SDK only records it in WithUA/SetUA (and uses w.ua for upload2C), so
//     dispatcher requests would otherwise be sent with resty's default
//     "go-resty/..." UA, which the WoPan edge rejects with HTTP 403
//     ("failed to list objs: request failed with status: 403 Forbidden").
//  2. A missing or too short access token must be refreshed before the first
//     request. Every wohome/api-user request body is AES encrypted with
//     accessToken[:16]; with an empty token the SDK fails with
//     "crypto/aes: invalid key size 0", which used to break refresh-token-only
//     configurations during FamilyUserCurrentEncode.
func (d *Wopan) initClient(ctx context.Context) error {
	d.client.SetUserAgent(wopan.DefaultUA)

	if len(d.AccessToken) < minAccessTokenLen {
		if d.RefreshToken == "" {
			return fmt.Errorf("an access token of at least %d characters or a refresh token is required", minAccessTokenLen)
		}
		if err := d.client.RefreshToken(); err != nil {
			return fmt.Errorf("refresh access token: %w", err)
		}
		if accessToken, _ := d.client.GetToken(); len(accessToken) < minAccessTokenLen {
			return errors.New("refresh access token: the server returned an invalid access token")
		}
	}

	if err := d.client.InitData(); err != nil {
		return fmt.Errorf("initialize user data: %w", err)
	}
	fml, err := d.client.FamilyUserCurrentEncode(func(req *resty.Request) {
		req.SetContext(ctx)
	})
	if err != nil {
		return fmt.Errorf("query default family: %w", err)
	}
	d.defaultFamilyID = strconv.Itoa(fml.DefaultHomeId)
	return nil
}

func (d *Wopan) Drop(ctx context.Context) error {
	return nil
}

func (d *Wopan) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	var res []model.Obj
	pageNum := 0
	pageSize := 100
	for {
		data, err := d.client.QueryAllFiles(d.getSpaceType(), dir.GetID(), pageNum, pageSize, 0, d.FamilyID, func(req *resty.Request) {
			req.SetContext(ctx)
		})
		if err != nil {
			return nil, err
		}
		objs, err := utils.SliceConvert(data.Files, fileToObj)
		if err != nil {
			return nil, err
		}
		res = append(res, objs...)
		if len(data.Files) < pageSize {
			break
		}
		pageNum++
	}
	return res, nil
}

func (d *Wopan) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if f, ok := file.(*Object); ok {
		res, err := d.client.GetDownloadUrlV2([]string{f.FID}, func(req *resty.Request) {
			req.SetContext(ctx)
		})
		if err != nil {
			return nil, err
		}
		return &model.Link{
			URL: res.List[0].DownloadUrl,
		}, nil
	}
	return nil, fmt.Errorf("unable to convert file to Object")
}

func (d *Wopan) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	familyID := d.FamilyID
	if familyID == "" {
		familyID = d.defaultFamilyID
	}
	_, err := d.client.CreateDirectory(d.getSpaceType(), parentDir.GetID(), dirName, familyID, func(req *resty.Request) {
		req.SetContext(ctx)
	})
	return err
}

func (d *Wopan) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	dirList := make([]string, 0)
	fileList := make([]string, 0)
	if srcObj.IsDir() {
		dirList = append(dirList, srcObj.GetID())
	} else {
		fileList = append(fileList, srcObj.GetID())
	}
	return d.client.MoveFile(dirList, fileList, dstDir.GetID(),
		d.getSpaceType(), d.getSpaceType(),
		d.FamilyID, d.FamilyID, func(req *resty.Request) {
			req.SetContext(ctx)
		})
}

func (d *Wopan) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	_type := 1
	if srcObj.IsDir() {
		_type = 0
	}
	return d.client.RenameFileOrDirectory(d.getSpaceType(), _type, srcObj.GetID(), newName, d.FamilyID, func(req *resty.Request) {
		req.SetContext(ctx)
	})
}

func (d *Wopan) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	dirList := make([]string, 0)
	fileList := make([]string, 0)
	if srcObj.IsDir() {
		dirList = append(dirList, srcObj.GetID())
	} else {
		fileList = append(fileList, srcObj.GetID())
	}
	return d.client.CopyFile(dirList, fileList, dstDir.GetID(),
		d.getSpaceType(), d.getSpaceType(),
		d.FamilyID, d.FamilyID, func(req *resty.Request) {
			req.SetContext(ctx)
		})
}

func (d *Wopan) Remove(ctx context.Context, obj model.Obj) error {
	dirList := make([]string, 0)
	fileList := make([]string, 0)
	if obj.IsDir() {
		dirList = append(dirList, obj.GetID())
	} else {
		fileList = append(fileList, obj.GetID())
	}
	return d.client.DeleteFile(d.getSpaceType(), dirList, fileList, func(req *resty.Request) {
		req.SetContext(ctx)
	})
}

func (d *Wopan) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	_, err := d.client.Upload2C(d.getSpaceType(), wopan.Upload2CFile{
		Name:        stream.GetName(),
		Size:        stream.GetSize(),
		Content:     driver.NewLimitedUploadStream(ctx, stream),
		ContentType: stream.GetMimetype(),
	}, dstDir.GetID(), d.FamilyID, wopan.Upload2COption{
		OnProgress: func(current, total int64) {
			up(100 * float64(current) / float64(total))
		},
		Ctx: ctx,
	})
	return err
}

func (d *Wopan) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	quota, err := d.client.QueryCloudUsageInfo()
	if err != nil {
		return nil, err
	}
	total, err := strconv.ParseInt(quota.UsageInfo.ByteTotalSize, 10, 64)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			UsedSpace:  quota.UsageInfo.ByteUsedSize,
		},
	}, nil
}

//func (d *Wopan) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
//	return nil, errs.NotSupport
//}

var _ driver.Driver = (*Wopan)(nil)
