package strm

import (
	"context"
	"errors"
	"strings"

	"github.com/OpenListTeam/OpenList/internal/driver"
	"github.com/OpenListTeam/OpenList/internal/errs"
	"github.com/OpenListTeam/OpenList/internal/fs"
	"github.com/OpenListTeam/OpenList/internal/model"
	"github.com/OpenListTeam/OpenList/pkg/utils"
)

type Strm struct {
	model.Storage
	Addition
	pathMap     map[string][]string
	autoFlatten bool
	oneKey      string
}

func (d *Strm) Config() driver.Config {
	return config
}

func (d *Strm) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Strm) Init(ctx context.Context) error {
	// 强制使用本地代理
	d.Storage.Proxy.WebdavPolicy = "native_proxy"
	if d.Paths == "" {
		return errors.New("paths is required")
	}
	d.pathMap = make(map[string][]string)
	for _, path := range strings.Split(d.Paths, "\n") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		k, v := getPair(path)
		d.pathMap[k] = append(d.pathMap[k], v)
	}
	if len(d.pathMap) == 1 {
		for k := range d.pathMap {
			d.oneKey = k
		}
		d.autoFlatten = true
	} else {
		d.oneKey = ""
		d.autoFlatten = false
	}

	if d.FilterFileTypes != "" {
		types := strings.Split(d.FilterFileTypes, ",")
		for _, ext := range types {
			ext = strings.ToLower(strings.TrimSpace(ext))
			if ext != "" {
				supportSuffix[ext] = struct{}{}
			}
		}
	}
	return nil
}

func (d *Strm) Drop(ctx context.Context) error {
	d.pathMap = nil
	return nil
}

func (d *Strm) Get(ctx context.Context, path string) (model.Obj, error) {
	if utils.PathEqual(path, "/") {
		return &model.Object{
			Name:     "Root",
			IsFolder: true,
			Path:     "/",
		}, nil
	}
	root, sub := d.getRootAndPath(path)
	dsts, ok := d.pathMap[root]
	if !ok {
		return nil, errs.ObjectNotFound
	}
	for _, dst := range dsts {
		obj, err := d.get(ctx, path, dst, sub)
		if err == nil {
			return obj, nil
		}
	}
	return nil, errs.ObjectNotFound
}

func (d *Strm) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	path := dir.GetPath()
	if utils.PathEqual(path, "/") && !d.autoFlatten {
		return d.listRoot(), nil
	}
	root, sub := d.getRootAndPath(path)
	dsts, ok := d.pathMap[root]
	if !ok {
		return nil, errs.ObjectNotFound
	}
	var objs []model.Obj
	fsArgs := &fs.ListArgs{NoLog: true, Refresh: args.Refresh}
	for _, dst := range dsts {
		tmp, err := d.list(ctx, dst, sub, fsArgs)
		if err == nil {
			objs = append(objs, tmp...)
		}
	}
	return objs, nil
}

func (d *Strm) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	root, sub := d.getRootAndPath(file.GetPath())
	dsts, ok := d.pathMap[root]
	if !ok {
		return nil, errs.ObjectNotFound
	}
	for _, dst := range dsts {
		link, err := d.link(ctx, dst, sub, args)
		if err == nil {
			return link, nil
		}
	}
	return nil, errs.ObjectNotFound
}

func (d *Strm) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	return errors.New("strm Driver cannot make dir")
}

func (d *Strm) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	return errors.New("strm Driver cannot move file")
}

func (d *Strm) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	return errors.New("strm Driver cannot rename file")
}

func (d *Strm) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return errors.New("strm Driver cannot copy file")
}

func (d *Strm) Remove(ctx context.Context, obj model.Obj) error {
	return errors.New("strm Driver cannot remove file")
}

func (d *Strm) Put(ctx context.Context, dstDir model.Obj, s model.FileStreamer, up driver.UpdateProgress) error {
	return errors.New("strm Driver cannot put file")
}

func (d *Strm) PutURL(ctx context.Context, dstDir model.Obj, name, url string) error {
	return errors.New("strm Driver cannot put file")
}

func (d *Strm) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	return nil, errs.NotImplement
}

func (d *Strm) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *Strm) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	return nil, errs.NotImplement
}

func (d *Strm) ArchiveDecompress(ctx context.Context, srcObj, dstDir model.Obj, args model.ArchiveDecompressArgs) error {
	return errs.NotImplement
}

var _ driver.Driver = (*Strm)(nil)
