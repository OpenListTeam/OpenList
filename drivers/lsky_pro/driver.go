package lsky_pro

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/go-resty/resty/v2"
)

// LskyPro maps a Lsky Pro (v2, API v1) gallery to a two level tree:
// the root holds albums (folders) and the images that belong to no album,
// each album holds its images.
type LskyPro struct {
	model.Storage
	Addition
	client *resty.Client
}

func (d *LskyPro) Config() driver.Config          { return config }
func (d *LskyPro) GetAddition() driver.Additional { return &d.Addition }

func (d *LskyPro) Init(ctx context.Context) error {
	d.Address = strings.TrimRight(d.Address, "/")
	d.client = base.NewRestyClient().
		SetBaseURL(d.Address).
		SetHeader("Accept", "application/json")

	if d.Token == "" {
		if d.Email == "" || d.Password == "" {
			return fmt.Errorf("token, or email and password, is required")
		}
		var data TokenData
		err := d.request(ctx, http.MethodPost, "/tokens", func(req *resty.Request) {
			req.SetFormData(map[string]string{"email": d.Email, "password": d.Password})
		}, &data)
		if err != nil {
			return err
		}
		d.Token = data.Token
		op.MustSaveDriverStorage(d)
	}
	d.client.SetAuthToken(strings.TrimPrefix(d.Token, "Bearer "))

	// connectivity check
	return d.request(ctx, http.MethodGet, "/profile", nil, nil)
}

func (d *LskyPro) Drop(ctx context.Context) error { return nil }

func (d *LskyPro) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	var objs []model.Obj
	albumID := dir.GetID()
	params := map[string]string{"order": "earliest"}
	if albumID == "0" || albumID == "" {
		albums, err := listAll[Album](ctx, d, "/albums", nil)
		if err != nil {
			return nil, err
		}
		for _, a := range albums {
			objs = append(objs, a.toObj())
		}
	} else {
		params["album_id"] = albumID
	}
	images, err := listAll[Image](ctx, d, "/images", params)
	if err != nil {
		return nil, err
	}
	for _, i := range images {
		objs = append(objs, i.toObj())
	}
	return objs, nil
}

func (d *LskyPro) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if img, ok := file.(*imageObj); ok && img.url != "" {
		return &model.Link{URL: img.url}, nil
	}
	return nil, errs.NotSupport
}

func (d *LskyPro) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	if id := parentDir.GetID(); id != "0" && id != "" {
		return nil, errs.NotSupport // albums cannot be nested
	}
	var album Album
	err := d.request(ctx, http.MethodPost, "/albums", func(req *resty.Request) {
		req.SetFormData(map[string]string{"name": dirName})
	}, &album)
	if err != nil {
		return nil, err
	}
	return album.toObj(), nil
}

func (d *LskyPro) Remove(ctx context.Context, obj model.Obj) error {
	path := "/images/" + obj.GetID()
	if obj.IsDir() {
		path = "/albums/" + obj.GetID()
	}
	return d.request(ctx, http.MethodDelete, path, nil, nil)
}

func (d *LskyPro) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	fields := map[string]string{}
	if id := dstDir.GetID(); id != "0" && id != "" {
		fields["album_id"] = id
	}
	if d.StrategyID != "" {
		fields["strategy_id"] = d.StrategyID
	}
	var img Image
	err := d.request(ctx, http.MethodPost, "/upload", func(req *resty.Request) {
		req.SetMultipartFormData(fields).SetFileReader("file", file.GetName(),
			driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
				Reader:         file,
				UpdateProgress: up,
			}))
	}, &img)
	if err != nil {
		return nil, err
	}
	if img.OriginName == "" {
		img.OriginName = file.GetName()
	}
	return img.toObj(), nil
}

var (
	_ driver.Driver      = (*LskyPro)(nil)
	_ driver.MkdirResult = (*LskyPro)(nil)
	_ driver.PutResult   = (*LskyPro)(nil)
	_ driver.Remove      = (*LskyPro)(nil)
)
