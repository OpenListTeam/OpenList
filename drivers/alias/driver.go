package alias

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/url"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
)

type Alias struct {
	model.Storage
	Addition
	rootOrder   []string
	pathMap     map[string][]string
	autoFlatten bool
	oneKey      string
}

func (d *Alias) Config() driver.Config {
	return config
}

func (d *Alias) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Alias) Init(ctx context.Context) error {
	if d.Paths == "" {
		return errors.New("paths is required")
	}
	if !utils.SliceContains(ValidReadConflictPolicy, d.ReadConflictPolicy) {
		d.ReadConflictPolicy = FirstRWP
	}
	if !utils.SliceContains(ValidWriteConflictPolicy, d.WriteConflictPolicy) {
		d.WriteConflictPolicy = DisabledWP
	}
	if !utils.SliceContains(ValidPutConflictPolicy, d.PutConflictPolicy) {
		d.PutConflictPolicy = DisabledWP
	}
	paths := strings.Split(d.Paths, "\n")
	d.rootOrder = make([]string, 0, len(paths))
	d.pathMap = make(map[string][]string)
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		k, v := getPair(path)
		if _, ok := d.pathMap[k]; !ok {
			d.rootOrder = append(d.rootOrder, k)
		}
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
	return nil
}

func (d *Alias) Drop(ctx context.Context) error {
	d.rootOrder = nil
	d.pathMap = nil
	return nil
}

func (Addition) GetRootPath() string {
	return "/"
}

func (d *Alias) Get(ctx context.Context, path string) (model.Obj, error) {
	root, sub := d.getRootAndPath(path)
	dsts, ok := d.pathMap[root]
	if !ok {
		return nil, errs.ObjectNotFound
	}
	var objs []model.Obj
	for _, dst := range dsts {
		p := stdpath.Join(dst, sub)
		obj, err := fs.Get(ctx, p, &fs.GetArgs{NoLog: true})
		if err != nil {
			continue
		}
		object := model.Object{
			Path:     path,
			Name:     obj.GetName(),
			Size:     obj.GetSize(),
			Modified: obj.ModTime(),
			IsFolder: obj.IsDir(),
			HashInfo: obj.GetHash(),
		}
		if !obj.IsDir() {
			if d.ProviderPassThrough {
				storage, e := fs.GetStorage(p, &fs.GetStoragesArgs{})
				provider := ""
				if e == nil {
					provider = storage.Config().Name
				}
				obj = &model.ObjectProvider{
					Object: object,
					Provider: model.Provider{
						Provider: provider,
					},
				}
			} else {
				obj = &object
			}
			obj = &BalancedObj{
				Obj:          obj,
				ExactReqPath: p,
			}
		} else {
			obj = &object
		}
		if d.ReadConflictPolicy == FirstRWP {
			return obj, nil
		} else {
			objs = append(objs, obj)
		}
	}
	if len(objs) == 0 {
		return nil, errs.ObjectNotFound
	}
	return objs[rand.Intn(len(objs))], nil
}

func (d *Alias) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	path := dir.GetPath()
	if utils.PathEqual(path, "/") && !d.autoFlatten {
		return d.listRoot(ctx, args.WithStorageDetails && d.DetailsPassThrough, args.Refresh), nil
	}
	root, sub := d.getRootAndPath(path)
	dsts, ok := d.pathMap[root]
	if !ok {
		return nil, errs.ObjectNotFound
	}
	objs := make(map[string][]model.Obj)
	for _, dst := range dsts {
		exactPath := stdpath.Join(dst, sub)
		tmp, err := fs.List(ctx, exactPath, &fs.ListArgs{
			NoLog:              true,
			Refresh:            args.Refresh,
			WithStorageDetails: args.WithStorageDetails && d.DetailsPassThrough,
		})
		if err == nil {
			tmp, err = utils.SliceConvert(tmp, func(obj model.Obj) (model.Obj, error) {
				objRes := model.Object{
					Name:     obj.GetName(),
					Path:     stdpath.Join(path, obj.GetName()),
					Size:     obj.GetSize(),
					Modified: obj.ModTime(),
					IsFolder: obj.IsDir(),
				}
				var objRet model.Obj
				if thumb, ok := model.GetThumb(obj); ok {
					objRet = &model.ObjThumb{
						Object: objRes,
						Thumbnail: model.Thumbnail{
							Thumbnail: thumb,
						},
					}
				} else {
					objRet = &objRes
				}
				if details, ok := model.GetStorageDetails(obj); ok {
					objRet = &model.ObjStorageDetails{
						Obj:                    objRet,
						StorageDetailsWithName: *details,
					}
				}
				if !objRet.IsDir() {
					objRet = &BalancedObj{
						Obj:          objRet,
						ExactReqPath: stdpath.Join(exactPath, objRet.GetName()),
					}
				}
				return objRet, nil
			})
		}
		if err == nil {
			for _, o := range tmp {
				objs[o.GetName()] = append(objs[o.GetName()], o)
			}
		}
	}
	ret := make([]model.Obj, 0, len(objs))
	for _, snObjs := range objs {
		if d.ReadConflictPolicy == RandomBalancedRP {
			ret = append(ret, snObjs[rand.Intn(len(snObjs))])
		} else {
			ret = append(ret, snObjs[0])
		}
	}
	return ret, nil
}

func (d *Alias) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	reqPath := GetExactReqPath(file)
	if reqPath == "" {
		return nil, errs.NotFile
	}
	// proxy || ftp,s3
	if common.GetApiUrl(ctx) == "" {
		args.Redirect = false
	}
	link, fi, err := d.link(ctx, reqPath, args)
	if err != nil {
		return nil, err
	}
	if link == nil {
		// 重定向且需要通过代理
		return &model.Link{
			URL: fmt.Sprintf("%s/p%s?sign=%s",
				common.GetApiUrl(ctx),
				utils.EncodePath(reqPath, true),
				sign.Sign(reqPath)),
		}, nil
	}

	resultLink := *link
	resultLink.SyncClosers = utils.NewSyncClosers(link)
	if args.Redirect {
		return &resultLink, nil
	}

	if resultLink.ContentLength == 0 {
		resultLink.ContentLength = fi.GetSize()
	}
	if d.DownloadConcurrency > 0 {
		resultLink.Concurrency = d.DownloadConcurrency
	}
	if d.DownloadPartSize > 0 {
		resultLink.PartSize = d.DownloadPartSize * utils.KB
	}
	return &resultLink, nil
}

func (d *Alias) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	reqPath := GetExactReqPath(args.Obj)
	if reqPath == "" {
		return nil, errs.NotImplement
	}
	storage, actualPath, err := op.GetStorageAndActualPath(reqPath)
	if err != nil {
		return nil, err
	}
	return op.Other(ctx, storage, model.FsOtherArgs{
		Path:   actualPath,
		Method: args.Method,
		Data:   args.Data,
	})
}

func (d *Alias) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	reqPath, err := d.getWritePath(ctx, parentDir, true)
	if err == nil {
		for _, path := range reqPath {
			err = errors.Join(err, fs.MakeDir(ctx, stdpath.Join(path, dirName)))
		}
		return err
	}
	return err
}

func (d *Alias) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	srcs, dsts, err := d.getCopyMovePath(ctx, srcObj, dstDir)
	if err != nil {
		return err
	}
	for i, src := range srcs {
		dst := dsts[i]
		_, e := fs.Move(ctx, src, dst)
		err = errors.Join(err, e)
	}
	return err
}

func (d *Alias) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	reqPath, err := d.getWritePath(ctx, srcObj, false)
	if err == nil {
		for _, path := range reqPath {
			err = errors.Join(err, fs.Rename(ctx, path, newName))
		}
		return err
	}
	return err
}

func (d *Alias) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	srcs, dsts, err := d.getCopyMovePath(ctx, srcObj, dstDir)
	if err != nil {
		return err
	}
	for i, src := range srcs {
		dst := dsts[i]
		_, e := fs.Copy(ctx, src, dst)
		err = errors.Join(err, e)
	}
	return err
}

func (d *Alias) Remove(ctx context.Context, obj model.Obj) error {
	reqPath, err := d.getWritePath(ctx, obj, false)
	if err == nil {
		for _, path := range reqPath {
			err = errors.Join(err, fs.Remove(ctx, path))
		}
		return err
	}
	return err
}

func (d *Alias) Put(ctx context.Context, dstDir model.Obj, s model.FileStreamer, up driver.UpdateProgress) error {
	reqPath, err := d.getPutPath(ctx, dstDir)
	if err == nil {
		if len(reqPath) == 1 {
			storage, reqActualPath, err := op.GetStorageAndActualPath(reqPath[0])
			if err != nil {
				return err
			}
			return op.Put(ctx, storage, reqActualPath, &stream.FileStream{
				Obj:      s,
				Mimetype: s.GetMimetype(),
				Reader:   s,
			}, up)
		} else {
			file, err := s.CacheFullAndWriter(nil, nil)
			if err != nil {
				return err
			}
			count := float64(len(reqPath) + 1)
			up(100 / count)
			for i, path := range reqPath {
				err = errors.Join(err, fs.PutDirectly(ctx, path, &stream.FileStream{
					Obj:      s,
					Mimetype: s.GetMimetype(),
					Reader:   file,
				}))
				up(float64(i+2) / float64(count) * 100)
				_, e := file.Seek(0, io.SeekStart)
				if e != nil {
					return errors.Join(err, e)
				}
			}
			return err
		}
	}
	return err
}

func (d *Alias) PutURL(ctx context.Context, dstDir model.Obj, name, url string) error {
	reqPath, err := d.getPutPath(ctx, dstDir)
	if err == nil {
		for _, path := range reqPath {
			err = errors.Join(err, fs.PutURL(ctx, path, name, url))
		}
		return err
	}
	return err
}

func (d *Alias) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	reqPath := GetExactReqPath(obj)
	if reqPath == "" {
		return nil, errs.NotFile
	}
	meta, err := d.getArchiveMeta(ctx, reqPath, args)
	if err == nil {
		return meta, nil
	}
	return nil, errs.NotImplement
}

func (d *Alias) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	reqPath := GetExactReqPath(obj)
	if reqPath == "" {
		return nil, errs.NotFile
	}
	l, err := d.listArchive(ctx, reqPath, args)
	if err == nil {
		return l, nil
	}
	return nil, errs.NotImplement
}

func (d *Alias) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	// alias的两个驱动，一个支持驱动提取，一个不支持，如何兼容？
	// 如果访问的是不支持驱动提取的驱动内的压缩文件，GetArchiveMeta就会返回errs.NotImplement，提取URL前缀就会是/ae，Extract就不会被调用
	// 如果访问的是支持驱动提取的驱动内的压缩文件，GetArchiveMeta就会返回有效值，提取URL前缀就会是/ad，Extract就会被调用
	reqPath := GetExactReqPath(obj)
	if reqPath == "" {
		return nil, errs.NotFile
	}
	link, err := d.extract(ctx, reqPath, args)
	if err != nil {
		return nil, errs.NotImplement
	}
	if link == nil {
		return &model.Link{
			URL: fmt.Sprintf("%s/ap%s?inner=%s&pass=%s&sign=%s",
				common.GetApiUrl(ctx),
				utils.EncodePath(reqPath, true),
				utils.EncodePath(args.InnerPath, true),
				url.QueryEscape(args.Password),
				sign.SignArchive(reqPath)),
		}, nil
	}
	resultLink := *link
	resultLink.SyncClosers = utils.NewSyncClosers(link)
	return &resultLink, nil
}

func (d *Alias) ArchiveDecompress(ctx context.Context, srcObj, dstDir model.Obj, args model.ArchiveDecompressArgs) error {
	srcs, dsts, err := d.getCopyMovePath(ctx, srcObj, dstDir)
	if err != nil {
		return err
	}
	for i, src := range srcs {
		dst := dsts[i]
		_, e := fs.ArchiveDecompress(ctx, src, dst, args)
		err = errors.Join(err, e)
	}
	return err
}

func (d *Alias) ResolveLinkCacheMode(path string) driver.LinkCacheMode {
	root, sub := d.getRootAndPath(path)
	dsts, ok := d.pathMap[root]
	if !ok {
		return 0
	}
	for _, dst := range dsts {
		storage, actualPath, err := op.GetStorageAndActualPath(stdpath.Join(dst, sub))
		if err != nil {
			continue
		}
		if storage.Config().CheckStatus && storage.GetStorage().Status != op.WORK {
			continue
		}
		mode := storage.Config().LinkCacheMode
		if mode == -1 {
			return storage.(driver.LinkCacheModeResolver).ResolveLinkCacheMode(actualPath)
		} else {
			return mode
		}
	}
	return 0
}

var _ driver.Driver = (*Alias)(nil)
