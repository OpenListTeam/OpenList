package alias

import (
	"context"
	"fmt"
	"net/url"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/internal/driver"
	"github.com/OpenListTeam/OpenList/internal/errs"
	"github.com/OpenListTeam/OpenList/internal/fs"
	"github.com/OpenListTeam/OpenList/internal/model"
	"github.com/OpenListTeam/OpenList/internal/op"
	"github.com/OpenListTeam/OpenList/internal/sign"
	"github.com/OpenListTeam/OpenList/pkg/utils"
	"github.com/OpenListTeam/OpenList/server/common"
)

func (d *Alias) listRoot() []model.Obj {
	var objs []model.Obj
	for k := range d.pathMap {
		obj := model.Object{
			Name:     k,
			IsFolder: true,
			Modified: d.Modified,
		}
		objs = append(objs, &obj)
	}
	return objs
}

// do others that not defined in Driver interface
func getPair(path string) (string, string) {
	//path = strings.TrimSpace(path)
	if strings.Contains(path, ":") {
		pair := strings.SplitN(path, ":", 2)
		if !strings.Contains(pair[0], "/") {
			return pair[0], pair[1]
		}
	}
	return stdpath.Base(path), path
}

func (d *Alias) getRootAndPath(path string) (string, string) {
	if d.autoFlatten {
		return d.oneKey, path
	}
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func (d *Alias) get(ctx context.Context, path string, dst, sub string) (model.Obj, error) {
	obj, err := fs.Get(ctx, stdpath.Join(dst, sub), &fs.GetArgs{NoLog: true})
	if err != nil {
		return nil, err
	}
	return &model.Object{
		Path:     path,
		Name:     obj.GetName(),
		Size:     obj.GetSize(),
		Modified: obj.ModTime(),
		IsFolder: obj.IsDir(),
		HashInfo: obj.GetHash(),
	}, nil
}

func (d *Alias) list(ctx context.Context, dst, sub string, args *fs.ListArgs) ([]model.Obj, error) {
	objs, err := fs.List(ctx, stdpath.Join(dst, sub), args)
	// the obj must implement the model.SetPath interface
	// return objs, err
	if err != nil {
		return nil, err
	}
	return utils.SliceConvert(objs, func(obj model.Obj) (model.Obj, error) {
		thumb, ok := model.GetThumb(obj)
		objRes := model.Object{
			Name:     obj.GetName(),
			Size:     obj.GetSize(),
			Modified: obj.ModTime(),
			IsFolder: obj.IsDir(),
		}
		if !ok {
			return &objRes, nil
		}
		return &model.ObjThumb{
			Object: objRes,
			Thumbnail: model.Thumbnail{
				Thumbnail: thumb,
			},
		}, nil
	})
}

func (d *Alias) link(ctx context.Context, dst, sub string, args model.LinkArgs) (*model.Link, error) {
	reqPath := stdpath.Join(dst, sub)
	// 参考 crypt 驱动
	storage, reqActualPath, err := op.GetStorageAndActualPath(reqPath)
	if err != nil {
		return nil, err
	}
	if _, ok := storage.(*Alias); !ok && !args.Redirect {
		link, _, err := op.Link(ctx, storage, reqActualPath, args)
		return link, err
	}
	_, err = fs.Get(ctx, reqPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		return nil, err
	}
	if common.ShouldProxy(storage, stdpath.Base(sub)) {
		link := &model.Link{
			URL: fmt.Sprintf("%s/p%s?sign=%s",
				common.GetApiUrl(args.HttpReq),
				utils.EncodePath(reqPath, true),
				sign.Sign(reqPath)),
		}
		if args.HttpReq != nil && d.ProxyRange {
			link.RangeReadCloser = common.NoProxyRange
		}
		return link, nil
	}
	link, _, err := op.Link(ctx, storage, reqActualPath, args)
	return link, err
}

func (d *Alias) getReqPath(ctx context.Context, obj model.Obj, isParent bool) ([]*string, error) {
	root, sub := d.getRootAndPath(obj.GetPath())
	if sub == "" && !isParent {
		return nil, errs.NotSupport
	}
	dsts, ok := d.pathMap[root]
	all := true
	if !ok {
		return nil, errs.ObjectNotFound
	}
	var reqPath []*string
	for _, dst := range dsts {
		path := stdpath.Join(dst, sub)
		_, err := fs.Get(ctx, path, &fs.GetArgs{NoLog: true})
		if err != nil {
			all = false
			if d.ProtectSameName && d.ParallelWrite && len(reqPath) >= 2 {
				return nil, errs.NotImplement
			}
			continue
		}
		if !d.ProtectSameName && !d.ParallelWrite {
			return []*string{&path}, nil
		}
		reqPath = append(reqPath, &path)
		if d.ProtectSameName && !d.ParallelWrite && len(reqPath) >= 2 {
			return nil, errs.NotImplement
		}
		if d.ProtectSameName && d.ParallelWrite && len(reqPath) >= 2 && !all {
			return nil, errs.NotImplement
		}
	}
	if len(reqPath) == 0 {
		return nil, errs.ObjectNotFound
	}
	return reqPath, nil
}

func (d *Alias) getArchiveMeta(ctx context.Context, dst, sub string, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	reqPath := stdpath.Join(dst, sub)
	storage, reqActualPath, err := op.GetStorageAndActualPath(reqPath)
	if err != nil {
		return nil, err
	}
	if _, ok := storage.(driver.ArchiveReader); ok {
		return op.GetArchiveMeta(ctx, storage, reqActualPath, model.ArchiveMetaArgs{
			ArchiveArgs: args,
			Refresh:     true,
		})
	}
	return nil, errs.NotImplement
}

func (d *Alias) listArchive(ctx context.Context, dst, sub string, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	reqPath := stdpath.Join(dst, sub)
	storage, reqActualPath, err := op.GetStorageAndActualPath(reqPath)
	if err != nil {
		return nil, err
	}
	if _, ok := storage.(driver.ArchiveReader); ok {
		return op.ListArchive(ctx, storage, reqActualPath, model.ArchiveListArgs{
			ArchiveInnerArgs: args,
			Refresh:          true,
		})
	}
	return nil, errs.NotImplement
}

func (d *Alias) extract(ctx context.Context, dst, sub string, args model.ArchiveInnerArgs) (*model.Link, error) {
	reqPath := stdpath.Join(dst, sub)
	storage, reqActualPath, err := op.GetStorageAndActualPath(reqPath)
	if err != nil {
		return nil, err
	}
	if _, ok := storage.(driver.ArchiveReader); ok {
		if _, ok := storage.(*Alias); !ok && !args.Redirect {
			link, _, err := op.DriverExtract(ctx, storage, reqActualPath, args)
			return link, err
		}
		_, err = fs.Get(ctx, reqPath, &fs.GetArgs{NoLog: true})
		if err != nil {
			return nil, err
		}
		if common.ShouldProxy(storage, stdpath.Base(sub)) {
			link := &model.Link{
				URL: fmt.Sprintf("%s/ap%s?inner=%s&pass=%s&sign=%s",
					common.GetApiUrl(args.HttpReq),
					utils.EncodePath(reqPath, true),
					utils.EncodePath(args.InnerPath, true),
					url.QueryEscape(args.Password),
					sign.SignArchive(reqPath)),
			}
			if args.HttpReq != nil && d.ProxyRange {
				link.RangeReadCloser = common.NoProxyRange
			}
			return link, nil
		}
		link, _, err := op.DriverExtract(ctx, storage, reqActualPath, args)
		return link, err
	}
	return nil, errs.NotImplement
}
