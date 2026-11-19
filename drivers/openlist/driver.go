package openlist

import (
	"context"
	"net/http"

	family "github.com/OpenListTeam/OpenList/v4/drivers/internal/openlist"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/go-resty/resty/v2"
)

type OpenList struct {
	model.Storage
	Addition
}

func (d *OpenList) Config() driver.Config { return config }

func (d *OpenList) GetAddition() driver.Additional { return &d.Addition }

func (d *OpenList) state() family.State {
	return family.State{
		Driver: d, Address: &d.Address, MetaPassword: &d.MetaPassword,
		Username: &d.Username, Password: &d.Password, Token: &d.Token,
		PassIP: d.PassIPToUpsteam, PassUserAgent: d.PassUAToUpsteam,
		ForwardArchives: d.ForwardArchiveReq,
		ForwardRefresh:  d.PassRefreshFlagToUpsteam, SendOverwrite: true,
	}
}

func (d *OpenList) Init(context.Context) error {
	return family.Init(d.state(), func(s family.State) (family.Identity, error) {
		var resp common.Resp[MeResp]
		_, _, err := s.Request("/me", http.MethodGet, func(req *resty.Request) { req.SetResult(&resp) })
		return family.Identity{
			Username: resp.Data.Username,
			Guest:    resp.Data.Role == model.GUEST,
		}, err
	})
}

func (d *OpenList) Drop(context.Context) error { return nil }

func (d *OpenList) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return family.List(d.state(), ctx, dir, args)
}

func (d *OpenList) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return family.Link(d.state(), ctx, file, args)
}

func (d *OpenList) MakeDir(ctx context.Context, parent model.Obj, name string) error {
	return family.MakeDir(d.state(), ctx, parent, name)
}

func (d *OpenList) Move(ctx context.Context, src, dst model.Obj) error {
	return family.Move(d.state(), ctx, src, dst)
}

func (d *OpenList) Rename(ctx context.Context, src model.Obj, name string) error {
	return family.Rename(d.state(), ctx, src, name)
}

func (d *OpenList) Copy(ctx context.Context, src, dst model.Obj) error {
	return family.Copy(d.state(), ctx, src, dst)
}

func (d *OpenList) Remove(ctx context.Context, obj model.Obj) error {
	return family.Remove(d.state(), ctx, obj)
}

func (d *OpenList) Put(ctx context.Context, dst model.Obj, src model.FileStreamer, up driver.UpdateProgress) error {
	return family.Put(d.state(), ctx, dst, src, up)
}

func (d *OpenList) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	return family.GetArchiveMeta(d.state(), ctx, obj, args)
}

func (d *OpenList) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	return family.ListArchive(d.state(), ctx, obj, args)
}

func (d *OpenList) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	return family.Extract(d.state(), ctx, obj, args)
}

func (d *OpenList) ArchiveDecompress(ctx context.Context, src, dst model.Obj, args model.ArchiveDecompressArgs) error {
	return family.ArchiveDecompress(d.state(), ctx, src, dst, args)
}

func (d *OpenList) ResolveLinkCacheMode(path string) driver.LinkCacheMode {
	return family.ResolveLinkCacheMode(d.state(), path)
}

var _ driver.Driver = (*OpenList)(nil)
