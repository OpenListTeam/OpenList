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
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
)

type Alias struct {
	model.Storage
	Addition
	rootOrder []string
	pathMap   map[string][]string
	root      model.Obj
}

func (d *Alias) Config() driver.Config {
	return config
}

func (d *Alias) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Alias) Init(ctx context.Context) error {
	paths := strings.Split(d.Paths, "\n")
	d.rootOrder = make([]string, 0, len(paths))
	d.pathMap = make(map[string][]string)
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		k, v := getPair(path)
		temp, ok := d.pathMap[k]
		if !ok {
			d.rootOrder = append(d.rootOrder, k)
		}
		d.pathMap[k] = append(temp, v)
	}

	switch len(d.rootOrder) {
	case 0:
		return errors.New("paths is required")
	case 1:
		paths := d.pathMap[d.rootOrder[0]]
		roots := make(BalancedObjs, 0, len(paths))
		roots = append(roots, &model.Object{
			Name:     "root",
			Path:     paths[0],
			IsFolder: true,
			Modified: d.Modified,
			Mask:     model.Locked,
		})
		for _, path := range paths[1:] {
			roots = append(roots, &model.Object{
				Path: path,
			})
		}
		d.root = roots
	default:
		d.root = &model.Object{
			Name:     "root",
			Path:     "/",
			IsFolder: true,
			Modified: d.Modified,
			Mask:     model.ReadOnly,
		}
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
	return nil
}

func (d *Alias) Drop(ctx context.Context) error {
	d.rootOrder = nil
	d.pathMap = nil
	d.root = nil
	return nil
}

func (d *Alias) GetRoot(ctx context.Context) (model.Obj, error) {
	if d.root == nil {
		return nil, errs.StorageNotInit
	}
	return d.root, nil
}

// 通过op.Get调用的话，path一定是子路径(/开头)
func (d *Alias) Get(ctx context.Context, path string) (model.Obj, error) {
	roots, sub := d.getRootsAndPath(path)
	if len(roots) == 0 {
		return nil, errs.ObjectNotFound
	}
	for idx, root := range roots {
		rawPath := stdpath.Join(root, sub)
		obj, err := fs.Get(ctx, rawPath, &fs.GetArgs{NoLog: true})
		if err != nil {
			continue
		}
		mask := model.GetObjMask(obj) &^ model.Temp
		if sub == "" {
			// 根目录
			mask |= model.Locked | model.Virtual
		}
		ret := model.Object{
			Path:     rawPath,
			Name:     obj.GetName(),
			Size:     obj.GetSize(),
			Modified: obj.ModTime(),
			IsFolder: obj.IsDir(),
			HashInfo: obj.GetHash(),
			Mask:     mask,
		}
		obj = &ret
		if d.ProviderPassThrough && !obj.IsDir() {
			if storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{}); err == nil {
				obj = &model.ObjectProvider{
					Object: ret,
					Provider: model.Provider{
						Provider: storage.Config().Name,
					},
				}
			}
		}

		roots = roots[idx+1:]
		var objs BalancedObjs
		if idx > 0 {
			objs = make(BalancedObjs, 0, len(roots)+2)
		} else {
			objs = make(BalancedObjs, 0, len(roots)+1)
		}
		objs = append(objs, obj)
		if idx > 0 {
			objs = append(objs, nil)
		}
		for _, d := range roots {
			objs = append(objs, &tempObj{model.Object{
				Path: stdpath.Join(d, sub),
			}})
		}
		return objs, nil
	}
	return nil, errs.ObjectNotFound
}

func (d *Alias) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	dirs, ok := dir.(BalancedObjs)
	if !ok {
		return d.listRoot(ctx, args.WithStorageDetails && d.DetailsPassThrough, args.Refresh), nil
	}

	// 因为alias是NoCache且Get方法不会返回NotSupport或NotImplement错误
	// 所以这里对象不会传回到alias，也就不需要返回BalancedObjs了
	objMap := make(map[string]model.Obj)
	for _, dir := range dirs {
		if dir == nil {
			continue
		}
		dirPath := dir.GetPath()
		tmp, err := fs.List(ctx, dirPath, &fs.ListArgs{
			NoLog:              true,
			Refresh:            args.Refresh,
			WithStorageDetails: args.WithStorageDetails && d.DetailsPassThrough,
		})
		if err != nil {
			continue
		}
		for _, obj := range tmp {
			name := obj.GetName()
			if _, exists := objMap[name]; exists {
				continue
			}
			mask := model.GetObjMask(obj) &^ model.Temp
			objRes := model.Object{
				Name:     name,
				Path:     stdpath.Join(dirPath, name),
				Size:     obj.GetSize(),
				Modified: obj.ModTime(),
				IsFolder: obj.IsDir(),
				Mask:     mask,
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
					Obj:            objRet,
					StorageDetails: details,
				}
			}
			objMap[name] = objRet
		}
	}
	objs := make([]model.Obj, 0, len(objMap))
	for _, obj := range objMap {
		objs = append(objs, obj)
	}
	if d.OrderBy == "" {
		sort := getAllSort(dirs)
		if sort.OrderBy != "" {
			model.SortFiles(objs, sort.OrderBy, sort.OrderDirection)
		}
		if d.ExtractFolder == "" && sort.ExtractFolder != "" {
			model.ExtractFolder(objs, sort.ExtractFolder)
		}
	}
	return objs, nil
}

func (d *Alias) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if d.ReadConflictPolicy == AllRWP && !args.Redirect {
		files, err := d.getAllObjs(ctx, file, getWriteAndPutFilterFunc(AllRWP))
		if err != nil {
			return nil, err
		}
		linkClosers := make([]io.Closer, 0, len(files))
		rrf := make([]model.RangeReaderIF, 0, len(files))
		for _, f := range files {
			link, fi, err := d.link(ctx, f.GetPath(), args)
			if err != nil {
				continue
			}
			if fi.GetSize() != files.GetSize() {
				_ = link.Close()
				continue
			}
			l := *link // 复制一份，避免修改到原始link
			if l.ContentLength == 0 {
				l.ContentLength = fi.GetSize()
			}
			if d.DownloadConcurrency > 0 {
				l.Concurrency = d.DownloadConcurrency
			}
			if d.DownloadPartSize > 0 {
				l.PartSize = d.DownloadPartSize * utils.KB
			}
			rr, err := stream.GetRangeReaderFromLink(l.ContentLength, &l)
			if err != nil {
				_ = link.Close()
				continue
			}
			linkClosers = append(linkClosers, link)
			rrf = append(rrf, rr)
		}
		rr := func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
			return rrf[rand.Intn(len(rrf))].RangeRead(ctx, httpRange)
		}
		return &model.Link{
			RangeReader: stream.RangeReaderFunc(rr),
			SyncClosers: utils.NewSyncClosers(linkClosers...),
		}, nil
	}

	var link *model.Link
	var fi model.Obj
	var err error
	files := file.(BalancedObjs)
	if d.ReadConflictPolicy == RandomBalancedRP || d.ReadConflictPolicy == AllRWP {
		rand.Shuffle(len(files), func(i, j int) {
			files[i], files[j] = files[j], files[i]
		})
	}
	for _, f := range files {
		if f == nil {
			continue
		}
		link, fi, err = d.link(ctx, f.GetPath(), args)
		if err == nil {
			if link == nil {
				// 重定向且需要通过代理
				return &model.Link{
					URL: fmt.Sprintf("%s/p%s?sign=%s",
						common.GetApiUrl(ctx),
						utils.EncodePath(f.GetPath(), true),
						sign.Sign(f.GetPath())),
				}, nil
			}
			break
		}
	}
	if err != nil {
		return nil, err
	}
	resultLink := *link // 复制一份，避免修改到原始link
	resultLink.Expiration = nil
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
	// Other 不应负载均衡，这是因为前端是否调用 /fs/other 的判断条件是返回的 provider 的值
	// 而 ProviderPassThrough 开启时，返回的 provider 固定为第一个 obj 的后端驱动
	storage, actualPath, err := op.GetStorageAndActualPath(args.Obj.GetPath())
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
	objs, err := d.getWriteObjs(ctx, parentDir)
	if err == nil {
		for _, obj := range objs {
			err = errors.Join(err, fs.MakeDir(ctx, stdpath.Join(obj.GetPath(), dirName)))
		}
	}
	return err
}

func (d *Alias) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	srcs, dsts, err := d.getMoveObjs(ctx, srcObj, dstDir)
	if err == nil {
		for i, dst := range dsts {
			src := srcs[i]
			_, e := fs.Move(ctx, src.GetPath(), dst.GetPath())
			err = errors.Join(err, e)
		}
		srcs = srcs[len(dsts):]
		for _, src := range srcs {
			e := fs.Remove(ctx, src.GetPath())
			err = errors.Join(err, e)
		}
	}
	return err
}

func (d *Alias) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	objs, err := d.getWriteObjs(ctx, srcObj)
	if err == nil {
		for _, obj := range objs {
			err = errors.Join(err, fs.Rename(ctx, obj.GetPath(), newName))
		}
	}
	return err
}

func (d *Alias) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	srcs, dsts, err := d.getCopyObjs(ctx, srcObj, dstDir)
	if err == nil {
		for i, src := range srcs {
			dst := dsts[i]
			_, e := fs.Copy(ctx, src.GetPath(), dst.GetPath())
			err = errors.Join(err, e)
		}
	}
	return err
}

func (d *Alias) Remove(ctx context.Context, obj model.Obj) error {
	objs, err := d.getWriteObjs(ctx, obj)
	if err == nil {
		for _, obj := range objs {
			err = errors.Join(err, fs.Remove(ctx, obj.GetPath()))
		}
	}
	return err
}

func (d *Alias) Put(ctx context.Context, dstDir model.Obj, s model.FileStreamer, up driver.UpdateProgress) error {
	objs, err := d.getPutObjs(ctx, dstDir)
	if err == nil {
		if len(objs) == 1 {
			storage, reqActualPath, err := op.GetStorageAndActualPath(objs.GetPath())
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
			count := float64(len(objs) + 1)
			up(100 / count)
			for i, obj := range objs {
				err = errors.Join(err, fs.PutDirectly(ctx, obj.GetPath(), &stream.FileStream{
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
	objs, err := d.getPutObjs(ctx, dstDir)
	if err == nil {
		for _, obj := range objs {
			err = errors.Join(err, fs.PutURL(ctx, obj.GetPath(), name, url))
		}
		return err
	}
	return err
}

func (d *Alias) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	reqPath := d.getBalancedPath(ctx, obj)
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
	reqPath := d.getBalancedPath(ctx, obj)
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
	reqPath := d.getBalancedPath(ctx, obj)
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
	srcs, dsts, err := d.getCopyObjs(ctx, srcObj, dstDir)
	if err == nil {
		for i, src := range srcs {
			dst := dsts[i]
			_, e := fs.ArchiveDecompress(ctx, src.GetPath(), dst.GetPath(), args)
			err = errors.Join(err, e)
		}
	}
	return err
}

func (d *Alias) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	if !d.DetailsPassThrough {
		return nil, errs.NotImplement
	}
	if len(d.rootOrder) != 1 {
		return nil, errs.NotImplement
	}
	backends := d.pathMap[d.rootOrder[0]]
	var storage driver.Driver
	for _, backend := range backends {
		s, err := fs.GetStorage(backend, &fs.GetStoragesArgs{})
		if err != nil {
			return nil, errs.NotImplement
		}
		if storage == nil {
			storage = s
		} else if storage.GetStorage().MountPath != s.GetStorage().MountPath {
			return nil, errs.NotImplement
		}
	}
	if storage == nil { // should never access
		return nil, errs.NotImplement
	}
	return op.GetStorageDetails(ctx, storage)
}

func (d *Alias) ResolveLinkCacheMode(path string) driver.LinkCacheMode {
	roots, sub := d.getRootsAndPath(path)
	if len(roots) == 0 {
		return 0
	}
	for _, root := range roots {
		storage, actualPath, err := op.GetStorageAndActualPath(stdpath.Join(root, sub))
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
