package alist_v3

import (
	"context"
	"net/http"

	family "github.com/OpenListTeam/OpenList/v4/drivers/internal/openlist"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/go-resty/resty/v2"
)

type AListV3 struct {
	model.Storage
	Addition
}

func (d *AListV3) Config() driver.Config { return config }

func (d *AListV3) GetAddition() driver.Additional { return &d.Addition }

func (d *AListV3) state() family.State {
	return family.State{
		Driver: d, Address: &d.Address, MetaPassword: &d.MetaPassword,
		Username: &d.Username, Password: &d.Password, Token: &d.Token,
		PassIP: d.PassIPToUpsteam, PassUserAgent: d.PassUAToUpsteam,
		ForwardArchives: d.ForwardArchiveReq,
	}
}

func (d *AListV3) Init(context.Context) error {
	return family.Init(d.state(), func(s family.State) (family.Identity, error) {
		var resp common.Resp[MeResp]
		_, _, err := s.Request("/me", http.MethodGet, func(req *resty.Request) { req.SetResult(&resp) })
		return family.Identity{
			Username: resp.Data.Username,
			Guest:    utils.SliceContains(resp.Data.Role, model.GUEST),
		}, err
	})
}

func (d *AListV3) Drop(context.Context) error { return nil }

func (d *AListV3) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return family.List(d.state(), ctx, dir, args)
}

func (d *AListV3) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return family.Link(d.state(), ctx, file, args)
}

func (d *AListV3) MakeDir(ctx context.Context, parent model.Obj, name string) error {
	return family.MakeDir(d.state(), ctx, parent, name)
}

func (d *AListV3) Move(ctx context.Context, src, dst model.Obj) error {
	return family.Move(d.state(), ctx, src, dst)
}

func (d *AListV3) Rename(ctx context.Context, src model.Obj, name string) error {
	return family.Rename(d.state(), ctx, src, name)
}

func (d *AListV3) Copy(ctx context.Context, src, dst model.Obj) error {
	return family.Copy(d.state(), ctx, src, dst)
}

func (d *AListV3) Remove(ctx context.Context, obj model.Obj) error {
	return family.Remove(d.state(), ctx, obj)
}

func (d *AListV3) Put(ctx context.Context, dst model.Obj, src model.FileStreamer, up driver.UpdateProgress) error {
	return family.Put(d.state(), ctx, dst, src, up)
}

func (d *AListV3) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	return family.GetArchiveMeta(d.state(), ctx, obj, args)
}

func (d *AListV3) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	return family.ListArchive(d.state(), ctx, obj, args)
}

func (d *AListV3) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	return family.Extract(d.state(), ctx, obj, args)
}

func (d *AListV3) ArchiveDecompress(ctx context.Context, src, dst model.Obj, args model.ArchiveDecompressArgs) error {
	return family.ArchiveDecompress(d.state(), ctx, src, dst, args)
}

func (d *AListV3) ResolveLinkCacheMode(path string) driver.LinkCacheMode {
	return family.ResolveLinkCacheMode(d.state(), path)
}

var _ driver.Driver = (*AListV3)(nil)
