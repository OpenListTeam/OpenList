package cnb_releases

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

type CnbReleases struct {
	model.Storage
	Addition
	ref *CnbReleases
}

func (d *CnbReleases) Config() driver.Config {
	return config
}

func (d *CnbReleases) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *CnbReleases) Init(ctx context.Context) error {
	return nil
}

func (d *CnbReleases) InitReference(storage driver.Driver) error {
	refStorage, ok := storage.(*CnbReleases)
	if ok {
		d.ref = refStorage
		return nil
	}
	return fmt.Errorf("ref: storage is not CnbReleases")
}

func (d *CnbReleases) Drop(ctx context.Context) error {
	d.ref = nil
	return nil
}

func (d *CnbReleases) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	dirID := dir.GetID()
	if dirID == "" {
		// get all releases for root dir
		var resp ReleaseList

		err := d.Request(http.MethodGet, "/{repo}/-/releases", func(req *resty.Request) {
			req.SetPathParam("repo", d.Repo)
		}, &resp)
		if err != nil {
			return nil, err
		}

		return utils.SliceConvert(resp, func(src Release) (model.Obj, error) {
			name := src.Name
			if d.UseTagName {
				name = src.TagName
			}
			return &model.Object{
				ID:       src.ID,
				Name:     name,
				Size:     d.sumAssetsSize(src.Assets),
				Ctime:    src.CreatedAt,
				Modified: src.UpdatedAt,
				IsFolder: true,
			}, nil
		})
	}

	var resp Release
	err := d.Request(http.MethodGet, "/{repo}/-/releases/{release_id}", func(req *resty.Request) {
		req.SetPathParam("repo", d.Repo)
		req.SetPathParam("release_id", dirID)
	}, &resp)
	if err != nil {
		return nil, err
	}

	return utils.SliceConvert(resp.Assets, func(src ReleaseAsset) (model.Obj, error) {
		return &Object{
			Object: model.Object{
				ID:       src.ID,
				Path:     src.Path,
				Name:     src.Name,
				Size:     src.Size,
				Ctime:    src.CreatedAt,
				Modified: src.UpdatedAt,
				IsFolder: false,
			},
			ParentID: dirID,
		}, nil
	})

}

func (d *CnbReleases) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return &model.Link{
		URL: "https://cnb.cool" + file.GetPath(),
	}, nil
}

func (d *CnbReleases) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	if parentDir.GetPath() == "/" {
		// create a new release
		branch := d.DefaultBranch
		if branch == "" {
			branch = "main" // fallback to "main" if not set
		}
		return d.Request(http.MethodPost, "/{repo}/-/releases", func(req *resty.Request) {
			req.SetPathParam("repo", d.Repo)
			req.SetBody(base.Json{
				"name":             dirName,
				"tag_name":         dirName,
				"target_commitish": branch,
			})
		}, nil)
	}
	return errs.NotImplement
}

func (d *CnbReleases) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *CnbReleases) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	if srcObj.IsDir() && !d.UseTagName {
		return d.Request(http.MethodPatch, "/{repo}/-/releases/{release_id}", func(req *resty.Request) {
			req.SetPathParam("repo", d.Repo)
			req.SetPathParam("release_id", srcObj.GetID())
			req.SetFormData(map[string]string{
				"name": newName,
			})
		}, nil)
	}
	return errs.NotImplement
}

func (d *CnbReleases) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *CnbReleases) Remove(ctx context.Context, obj model.Obj) error {
	if obj.IsDir() {
		return d.Request(http.MethodDelete, "/{repo}/-/releases/{release_id}", func(req *resty.Request) {
			req.SetPathParam("repo", d.Repo)
			req.SetPathParam("release_id", obj.GetID())
		}, nil)
	}
	if o, ok := obj.(*Object); ok {
		return d.Request(http.MethodDelete, "/{repo}/-/releases/{release_id}/assets/{asset_id}", func(req *resty.Request) {
			req.SetPathParam("repo", d.Repo)
			req.SetPathParam("release_id", o.ParentID)
			req.SetPathParam("asset_id", obj.GetID())
		}, nil)
	} else {
		return fmt.Errorf("unable to get release ID")
	}
}

func (d *CnbReleases) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	// 1. get upload info
	var resp ReleaseAssetUploadURL
	err := d.Request(http.MethodPost, "/{repo}/-/releases/{release_id}/asset-upload-url", func(req *resty.Request) {
		req.SetPathParam("repo", d.Repo)
		req.SetPathParam("release_id", dstDir.GetID())
		req.SetBody(base.Json{
			"asset_name": file.GetName(),
			"overwrite":  true,
			"size":       file.GetSize(),
		})
	}, &resp)
	if err != nil {
		return err
	}

	// 2. upload file
	// use multipart to create form file
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_, err = w.CreateFormFile("file", file.GetName())
	if err != nil {
		return err
	}
	headSize := b.Len()
	err = w.Close()
	if err != nil {
		return err
	}
	head := bytes.NewReader(b.Bytes()[:headSize])
	tail := bytes.NewReader(b.Bytes()[headSize:])
	r := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
		Reader: &driver.SimpleReaderWithSize{
			Reader: io.MultiReader(head, file, tail),
			Size:   int64(b.Len()) + file.GetSize(),
		},
		UpdateProgress: up,
	})

	// use net/http to upload file
	ctxWithTimeout, cancel := context.WithTimeout(ctx, time.Duration(resp.ExpiresInSec+1)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctxWithTimeout, http.MethodPost, resp.UploadURL, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", base.UserAgent)
	httpResp, err := base.HttpClient.Do(req)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("upload file failed: %s", httpResp.Status)
	}

	// 3. verify upload
	return d.Request(http.MethodPost, resp.VerifyURL, nil, nil)
}

var _ driver.Driver = (*CnbReleases)(nil)
