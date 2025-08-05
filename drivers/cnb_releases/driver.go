package cnb_releases

import (
	"context"
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

type CnbReleases struct {
	model.Storage
	Addition
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

func (d *CnbReleases) Drop(ctx context.Context) error {
	return nil
}

func (d *CnbReleases) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if dir.GetPath() == "/" {
		// get all tags for root dir
		var resp TagList
		err := d.Request(http.MethodGet, "/{repo}/-/git/tags", func(req *resty.Request) {
			req.SetPathParam("repo", d.Repo)
		}, &resp)
		if err != nil {
			return nil, err
		}

		return utils.SliceConvert(resp, func(src Tag) (model.Obj, error) {
			return &model.Object{
				ID:       src.Target,
				Name:     src.Name,
				Modified: src.Commit.Commit.Committer.Date,
				IsFolder: src.TargetType == "tag",
			}, nil
		})
	} else {
		// get release info by tag name
		var resp Release
		err := d.Request(http.MethodGet, "/{repo}/-/releases/tags/{tag}", func(req *resty.Request) {
			req.SetPathParam("repo", d.Repo)
			req.SetPathParam("tag", dir.GetName())
		}, &resp)
		if err != nil {
			return nil, err
		}
		// return assets plane
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

func (d *CnbReleases) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	// TODO upload file, optional
	return nil, errs.NotImplement
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
