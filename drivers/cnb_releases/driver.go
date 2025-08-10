package cnb_releases

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

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
	// TODO login / refresh token
	//op.MustSaveDriverStorage(d)
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
	if dir.GetPath() == "/" {
		// // get all tags for root dir
		// var resp TagList
		// err := d.Request(http.MethodGet, "/{repo}/-/git/tags", func(req *resty.Request) {
		// 	req.SetPathParam("repo", d.Repo)
		// }, &resp)
		// if err != nil {
		// 	return nil, err
		// }

		// return utils.SliceConvert(resp, func(src Tag) (model.Obj, error) {
		// 	return &model.Object{
		// 		ID:       src.Target,
		// 		Name:     src.Name,
		// 		Modified: src.Commit.Commit.Committer.Date,
		// 		IsFolder: src.TargetType == "tag",
		// 	}, nil
		// })

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
	} else {
		// // get release info by tag name
		// var resp Release
		// err := d.Request(http.MethodGet, "/{repo}/-/releases/tags/{tag}", func(req *resty.Request) {
		// 	req.SetPathParam("repo", d.Repo)
		// 	req.SetPathParam("tag", dir.GetName())
		// }, &resp)
		// if err != nil {
		// 	return nil, err
		// }

		// get release info by release id
		releaseID := dir.GetID()
		if releaseID == "" {
			return nil, errs.ObjectNotFound
		}
		var resp Release
		err := d.Request(http.MethodGet, "/{repo}/-/releases/{release_id}", func(req *resty.Request) {
			req.SetPathParam("repo", d.Repo)
			req.SetPathParam("release_id", releaseID)
		}, &resp)
		if err != nil {
			return nil, err
		}

		return utils.SliceConvert(resp.Assets, func(src ReleaseAsset) (model.Obj, error) {
			return &model.Object{
				ID:       src.ID,
				Path:     src.Path, // for Link only
				Name:     src.Name,
				Size:     src.Size,
				Ctime:    src.CreatedAt,
				Modified: src.UpdatedAt,
				IsFolder: false,
			}, nil
		})
	}
}

func (d *CnbReleases) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return &model.Link{
		URL: "https://cnb.cool" + file.GetPath(),
	}, nil
}

func (d *CnbReleases) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	// TODO create folder, optional
	return nil, errs.NotImplement
}

func (d *CnbReleases) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	// TODO move obj, optional
	return nil, errs.NotImplement
}

func (d *CnbReleases) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	// TODO rename obj, optional
	return nil, errs.NotImplement
}

func (d *CnbReleases) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	// TODO copy obj, optional
	return nil, errs.NotImplement
}

func (d *CnbReleases) Remove(ctx context.Context, obj model.Obj) error {
	// TODO remove obj, optional
	return errs.NotImplement
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
	// var b bytes.Buffer
	// w := multipart.NewWriter(&b)
	// fw, err := w.CreateFormFile("file", file.GetName())
	// if err != nil {
	// 	return err
	// }
	// _, err = utils.CopyWithBuffer(fw, file)
	// if err != nil {
	// 	return err
	// }
	// err = w.Close()
	// if err != nil {
	// 	return err
	// }

	// use io.Pipe to stream file upload
	pr, pw := io.Pipe()
	w := multipart.NewWriter(pw)
	errChan := make(chan error, 1)
	go func() {
		defer pw.Close()
		defer w.Close()

		// Create form file field
		fw, err := w.CreateFormFile("file", file.GetName())
		if err != nil {
			errChan <- err
			pw.CloseWithError(err)
			return
		}

		// Copy file content with progress tracking
		if up != nil {
			// Create progress wrapper
			uprd := &driver.ReaderUpdatingProgress{
				Reader:         file,
				UpdateProgress: up,
			}
			_, err = utils.CopyWithBuffer(fw, uprd)
		} else {
			_, err = utils.CopyWithBuffer(fw, file)
		}

		if err != nil {
			errChan <- err
			pw.CloseWithError(err)
			return
		}

		errChan <- nil
	}()

	// use net/http to upload file
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resp.UploadURL, pr)
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

func (d *CnbReleases) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	// TODO get archive file meta-info, return errs.NotImplement to use an internal archive tool, optional
	return nil, errs.NotImplement
}

func (d *CnbReleases) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	// TODO list args.InnerPath in the archive obj, return errs.NotImplement to use an internal archive tool, optional
	return nil, errs.NotImplement
}

func (d *CnbReleases) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	// TODO return link of file args.InnerPath in the archive obj, return errs.NotImplement to use an internal archive tool, optional
	return nil, errs.NotImplement
}

func (d *CnbReleases) ArchiveDecompress(ctx context.Context, srcObj, dstDir model.Obj, args model.ArchiveDecompressArgs) ([]model.Obj, error) {
	// TODO extract args.InnerPath path in the archive srcObj to the dstDir location, optional
	// a folder with the same name as the archive file needs to be created to store the extracted results if args.PutIntoNewDir
	// return errs.NotImplement to use an internal archive tool
	return nil, errs.NotImplement
}

//func (d *Template) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
//	return nil, errs.NotSupport
//}

var _ driver.Driver = (*CnbReleases)(nil)
