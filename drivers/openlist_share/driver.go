package openlist_share

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/go-resty/resty/v2"
)

type OpenListShare struct {
	model.Storage
	Addition
	serverArchivePreview bool
}

func (d *OpenListShare) Config() driver.Config {
	return config
}

func (d *OpenListShare) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *OpenListShare) Init(ctx context.Context) error {
	d.Addition.Address = strings.TrimSuffix(d.Addition.Address, "/")
	var settings common.Resp[map[string]string]
	_, _, err := d.request("/public/settings", http.MethodGet, func(req *resty.Request) {
		req.SetResult(&settings)
	})
	if err != nil {
		return err
	}
	d.serverArchivePreview = settings.Data["share_archive_preview"] == "true"
	return nil
}

func (d *OpenListShare) Drop(ctx context.Context) error {
	return nil
}

func (d *OpenListShare) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	var resp common.Resp[FsListResp]
	_, _, err := d.request("/fs/list", http.MethodPost, func(req *resty.Request) {
		req.SetResult(&resp).SetBody(ListReq{
			PageReq: model.PageReq{
				Page:    1,
				PerPage: 0,
			},
			Path:     stdpath.Join(fmt.Sprintf("/@s/%s", d.ShareId), dir.GetPath()),
			Password: d.Pwd,
			Refresh:  false,
		})
	})
	if err != nil {
		return nil, err
	}
	var files []model.Obj
	for _, f := range resp.Data.Content {
		file := model.ObjThumb{
			Object: model.Object{
				Name:     f.Name,
				Path:     stdpath.Join(dir.GetPath(), f.Name),
				Modified: f.Modified,
				Ctime:    f.Created,
				Size:     f.Size,
				IsFolder: f.IsDir,
				HashInfo: utils.FromString(f.HashInfo),
			},
			Thumbnail: model.Thumbnail{Thumbnail: f.Thumb},
		}
		files = append(files, &file)
	}
	return files, nil
}

func (d *OpenListShare) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	path := utils.FixAndCleanPath(stdpath.Join(d.ShareId, file.GetPath()))
	u := fmt.Sprintf("%s/sd%s?pwd=%s", d.Address, path, d.Pwd)
	return &model.Link{URL: u}, nil
}

func (d *OpenListShare) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	if !d.serverArchivePreview || !d.ForwardArchiveReq {
		return nil, errs.NotImplement
	}
	var resp common.Resp[ArchiveMetaResp]
	_, code, err := d.request("/fs/archive/meta", http.MethodPost, func(req *resty.Request) {
		req.SetResult(&resp).SetBody(ArchiveMetaReq{
			ArchivePass: args.Password,
			Path:        stdpath.Join(fmt.Sprintf("/@s/%s", d.ShareId), obj.GetPath()),
			Password:    d.Pwd,
			Refresh:     false,
		})
	})
	if code == 202 {
		return nil, errs.WrongArchivePassword
	}
	if err != nil {
		return nil, err
	}
	var tree []model.ObjTree
	if resp.Data.Content != nil {
		tree = make([]model.ObjTree, 0, len(resp.Data.Content))
		for _, content := range resp.Data.Content {
			tree = append(tree, &content)
		}
	}
	return &model.ArchiveMetaInfo{
		Comment:   resp.Data.Comment,
		Encrypted: resp.Data.Encrypted,
		Tree:      tree,
	}, nil
}

func (d *OpenListShare) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	if !d.serverArchivePreview || !d.ForwardArchiveReq {
		return nil, errs.NotImplement
	}
	var resp common.Resp[ArchiveListResp]
	_, code, err := d.request("/fs/archive/list", http.MethodPost, func(req *resty.Request) {
		req.SetResult(&resp).SetBody(ArchiveListReq{
			ArchiveMetaReq: ArchiveMetaReq{
				ArchivePass: args.Password,
				Path:        stdpath.Join(fmt.Sprintf("/@s/%s", d.ShareId), obj.GetPath()),
				Password:    d.Pwd,
				Refresh:     false,
			},
			PageReq: model.PageReq{
				Page:    1,
				PerPage: 0,
			},
			InnerPath: args.InnerPath,
		})
	})
	if code == 202 {
		return nil, errs.WrongArchivePassword
	}
	if err != nil {
		return nil, err
	}
	var files []model.Obj
	for _, f := range resp.Data.Content {
		file := model.ObjThumb{
			Object: model.Object{
				Name:     f.Name,
				Modified: f.Modified,
				Ctime:    f.Created,
				Size:     f.Size,
				IsFolder: f.IsDir,
				HashInfo: utils.FromString(f.HashInfo),
			},
			Thumbnail: model.Thumbnail{Thumbnail: f.Thumb},
		}
		files = append(files, &file)
	}
	return files, nil
}

func (d *OpenListShare) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	if !d.serverArchivePreview || !d.ForwardArchiveReq {
		return nil, errs.NotSupport
	}
	path := utils.FixAndCleanPath(stdpath.Join(d.ShareId, obj.GetPath()))
	u := fmt.Sprintf("%s/sad%s?pwd=%s&inner=%s&pass=%s",
		d.Address,
		path,
		d.Pwd,
		utils.EncodePath(args.InnerPath, true),
		url.QueryEscape(args.Password))
	return &model.Link{URL: u}, nil
}

var _ driver.Driver = (*OpenListShare)(nil)
