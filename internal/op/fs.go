package op

import (
	"context"
	stderrors "errors"
	stdpath "path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

var listG singleflight.Group[[]model.Obj]

// List files in storage, not contains virtual file
func List(ctx context.Context, storage driver.Driver, path string, args model.ListArgs) ([]model.Obj, error) {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return nil, errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	path = utils.FixAndCleanPath(path)
	log.Debugf("op.List %s", path)
	key := Key(storage, path)
	if !args.Refresh {
		if dirCache, exists := Cache.dirCache.Get(key); exists {
			log.Debugf("use cache when list %s", path)
			return dirCache.GetSortedObjects(storage), nil
		}
	}

	dir, err := GetUnwrap(ctx, storage, path)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get dir")
	}
	log.Debugf("list dir: %+v", dir)
	if !dir.IsDir() {
		return nil, errors.WithStack(errs.NotFolder)
	}

	objs, err, _ := listG.Do(key, func() ([]model.Obj, error) {
		files, err := storage.List(ctx, dir, args)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list objs")
		}
		// set path
		for _, f := range files {
			if s, ok := f.(model.SetPath); ok && f.GetPath() == "" && dir.GetPath() != "" {
				s.SetPath(stdpath.Join(dir.GetPath(), f.GetName()))
			}
		}
		// warp obj name
		model.WrapObjsName(files)
		// call hooks
		go func(reqPath string, files []model.Obj) {
			HandleObjsUpdateHook(context.WithoutCancel(ctx), reqPath, files)
		}(utils.GetFullPath(storage.GetStorage().MountPath, path), files)

		// sort objs
		if storage.Config().LocalSort {
			model.SortFiles(files, storage.GetStorage().OrderBy, storage.GetStorage().OrderDirection)
		}
		model.ExtractFolder(files, storage.GetStorage().ExtractFolder)

		if !storage.Config().NoCache {
			if len(files) > 0 {
				log.Debugf("set cache: %s => %+v", key, files)
				ttl := time.Minute * time.Duration(storage.GetStorage().CacheExpiration)
				Cache.dirCache.SetWithTTL(key, newDirectoryCache(files), ttl)
			} else {
				log.Debugf("del cache: %s", key)
				Cache.deleteDirectoryTree(key)
			}
		}
		return files, nil
	})
	return objs, err
}

// Get object from list of files
func Get(ctx context.Context, storage driver.Driver, path string) (model.Obj, error) {
	path = utils.FixAndCleanPath(path)
	log.Debugf("op.Get %s", path)

	// get the obj directly without list so that we can reduce the io
	if g, ok := storage.(driver.Getter); ok {
		obj, err := g.Get(ctx, path)
		if err == nil {
			return model.WrapObjName(obj), nil
		}
		if !errs.IsNotImplementError(err) && !errs.IsNotSupportError(err) {
			return nil, errors.WithMessage(err, "failed to get obj")
		}
	}

	// is root folder
	if utils.PathEqual(path, "/") {
		var rootObj model.Obj
		if getRooter, ok := storage.(driver.GetRooter); ok {
			obj, err := getRooter.GetRoot(ctx)
			if err != nil {
				return nil, errors.WithMessage(err, "failed get root obj")
			}
			rootObj = obj
		} else {
			switch r := storage.GetAddition().(type) {
			case driver.IRootId:
				rootObj = &model.Object{
					ID:       r.GetRootId(),
					Name:     RootName,
					Size:     0,
					Modified: storage.GetStorage().Modified,
					IsFolder: true,
				}
			case driver.IRootPath:
				rootObj = &model.Object{
					Path:     r.GetRootPath(),
					Name:     RootName,
					Size:     0,
					Modified: storage.GetStorage().Modified,
					IsFolder: true,
				}
			default:
				return nil, errors.Errorf("please implement IRootPath or IRootId or GetRooter method")
			}
		}
		if rootObj == nil {
			return nil, errors.Errorf("please implement IRootPath or IRootId or GetRooter method")
		}
		return &model.ObjWrapName{
			Name: RootName,
			Obj:  rootObj,
		}, nil
	}

	// not root folder
	dir, name := stdpath.Split(path)
	files, err := List(ctx, storage, dir, model.ListArgs{})
	if err != nil {
		return nil, errors.WithMessage(err, "failed get parent list")
	}
	for _, f := range files {
		if f.GetName() == name {
			return f, nil
		}
	}
	log.Debugf("cant find obj with name: %s", name)
	return nil, errors.WithStack(errs.ObjectNotFound)
}

func GetUnwrap(ctx context.Context, storage driver.Driver, path string) (model.Obj, error) {
	obj, err := Get(ctx, storage, path)
	if err != nil {
		return nil, err
	}
	return model.UnwrapObj(obj), err
}

var linkG = singleflight.Group[*objWithLink]{}

// Link get link, if is an url. should have an expiry time
func Link(ctx context.Context, storage driver.Driver, path string, args model.LinkArgs) (*model.Link, model.Obj, error) {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return nil, nil, errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}

	mode := storage.Config().LinkCacheMode
	if mode == -1 {
		mode = storage.(driver.LinkCacheModeResolver).ResolveLinkCacheMode(path)
	}
	typeKey := args.Type
	if mode&driver.LinkCacheIP == driver.LinkCacheIP {
		typeKey += "/" + args.IP
	}
	if mode&driver.LinkCacheUA == driver.LinkCacheUA {
		typeKey += "/" + args.Header.Get("User-Agent")
	}
	key := Key(storage, path)
	if ol, exists := Cache.linkCache.GetType(key, typeKey); exists {
		if ol.link.Expiration != nil ||
			ol.link.SyncClosers.AcquireReference() || !ol.link.RequireReference {
			return ol.link, ol.obj, nil
		}
	}

	fn := func() (*objWithLink, error) {
		file, err := GetUnwrap(ctx, storage, path)
		if err != nil {
			return nil, errors.WithMessage(err, "failed to get file")
		}
		if file.IsDir() {
			return nil, errors.WithStack(errs.NotFile)
		}

		link, err := storage.Link(ctx, file, args)
		if err != nil {
			return nil, errors.Wrapf(err, "failed get link")
		}
		ol := &objWithLink{link: link, obj: file}
		if link.Expiration != nil {
			Cache.linkCache.SetTypeWithTTL(key, typeKey, ol, *link.Expiration)
		} else {
			Cache.linkCache.SetTypeWithExpirable(key, typeKey, ol, &link.SyncClosers)
		}
		return ol, nil
	}
	retry := 0
	for {
		ol, err, _ := linkG.Do(key+"/"+typeKey, fn)
		if err != nil {
			return nil, nil, err
		}
		if ol.link.SyncClosers.AcquireReference() || !ol.link.RequireReference {
			if retry > 1 {
				log.Warnf("Link retry successed after %d times: %s %s", retry, key, typeKey)
			}
			return ol.link, ol.obj, nil
		}
		retry++
	}
}

// Other api
func Other(ctx context.Context, storage driver.Driver, args model.FsOtherArgs) (interface{}, error) {
	obj, err := GetUnwrap(ctx, storage, args.Path)
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to get obj")
	}
	if o, ok := storage.(driver.Other); ok {
		return o.Other(ctx, model.OtherArgs{
			Obj:    obj,
			Method: args.Method,
			Data:   args.Data,
		})
	} else {
		return nil, errs.NotImplement
	}
}

var mkdirG singleflight.Group[any]

func MakeDir(ctx context.Context, storage driver.Driver, path string, lazyCache ...bool) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	path = utils.FixAndCleanPath(path)
	key := Key(storage, path)
	_, err, _ := mkdirG.Do(key, func() (any, error) {
		// check if dir exists
		f, err := GetUnwrap(ctx, storage, path)
		if err != nil {
			if errs.IsObjectNotFound(err) {
				parentPath, dirName := stdpath.Split(path)
				err = MakeDir(ctx, storage, parentPath)
				if err != nil {
					return nil, errors.WithMessagef(err, "failed to make parent dir [%s]", parentPath)
				}
				parentDir, err := GetUnwrap(ctx, storage, parentPath)
				// this should not happen
				if err != nil {
					return nil, errors.WithMessagef(err, "failed to get parent dir [%s]", parentPath)
				}

				switch s := storage.(type) {
				case driver.MkdirResult:
					var newObj model.Obj
					newObj, err = s.MakeDir(ctx, parentDir, dirName)
					if err == nil {
						if newObj != nil {
							if !storage.Config().NoCache {
								if dirCache, exist := Cache.dirCache.Get(Key(storage, parentPath)); exist {
									dirCache.UpdateObject("", newObj)
								}
							}
						} else if !utils.IsBool(lazyCache...) {
							Cache.DeleteDirectory(storage, parentPath)
						}
					}
				case driver.Mkdir:
					err = s.MakeDir(ctx, parentDir, dirName)
					if err == nil && !utils.IsBool(lazyCache...) {
						Cache.DeleteDirectory(storage, parentPath)
					}
				default:
					return nil, errs.NotImplement
				}
				return nil, errors.WithStack(err)
			}
			return nil, errors.WithMessage(err, "failed to check if dir exists")
		}
		// dir exists
		if f.IsDir() {
			return nil, nil
		}
		// dir to make is a file
		return nil, errors.New("file exists")
	})
	return err
}

func Move(ctx context.Context, storage driver.Driver, srcPath, dstDirPath string, lazyCache ...bool) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	srcPath = utils.FixAndCleanPath(srcPath)
	srcDirPath := stdpath.Dir(srcPath)
	dstDirPath = utils.FixAndCleanPath(dstDirPath)
	if dstDirPath == srcDirPath {
		return stderrors.New("move in place")
	}
	srcRawObj, err := Get(ctx, storage, srcPath)
	if err != nil {
		return errors.WithMessage(err, "failed to get src object")
	}
	srcObj := model.UnwrapObj(srcRawObj)
	dstDir, err := GetUnwrap(ctx, storage, dstDirPath)
	if err != nil {
		return errors.WithMessage(err, "failed to get dst dir")
	}

	switch s := storage.(type) {
	case driver.MoveResult:
		var newObj model.Obj
		newObj, err = s.Move(ctx, srcObj, dstDir)
		if err == nil {
			Cache.removeDirectoryObject(storage, srcDirPath, srcRawObj)
			if newObj != nil {
				Cache.addDirectoryObject(storage, dstDirPath, model.WrapObjName(newObj))
			} else if !utils.IsBool(lazyCache...) {
				Cache.DeleteDirectory(storage, dstDirPath)
			}
		}
	case driver.Move:
		err = s.Move(ctx, srcObj, dstDir)
		if err == nil {
			Cache.removeDirectoryObject(storage, srcDirPath, srcRawObj)
			if !utils.IsBool(lazyCache...) {
				Cache.DeleteDirectory(storage, dstDirPath)
			}
		}
	default:
		return errs.NotImplement
	}
	return errors.WithStack(err)
}

func Rename(ctx context.Context, storage driver.Driver, srcPath, dstName string, lazyCache ...bool) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	srcPath = utils.FixAndCleanPath(srcPath)
	srcRawObj, err := Get(ctx, storage, srcPath)
	if err != nil {
		return errors.WithMessage(err, "failed to get src object")
	}
	srcObj := model.UnwrapObj(srcRawObj)

	switch s := storage.(type) {
	case driver.RenameResult:
		var newObj model.Obj
		newObj, err = s.Rename(ctx, srcObj, dstName)
		if err == nil {
			srcDirPath := stdpath.Dir(srcPath)
			if newObj != nil {
				Cache.updateDirectoryObject(storage, srcDirPath, srcRawObj, model.WrapObjName(newObj))
			} else {
				Cache.removeDirectoryObject(storage, srcDirPath, srcRawObj)
				if !utils.IsBool(lazyCache...) {
					Cache.DeleteDirectory(storage, srcDirPath)
				}
			}
		}
	case driver.Rename:
		err = s.Rename(ctx, srcObj, dstName)
		if err == nil {
			srcDirPath := stdpath.Dir(srcPath)
			Cache.removeDirectoryObject(storage, srcDirPath, srcRawObj)
			if !utils.IsBool(lazyCache...) {
				Cache.DeleteDirectory(storage, srcDirPath)
			}
		}
	default:
		return errs.NotImplement
	}
	return errors.WithStack(err)
}

// Copy Just copy file[s] in a storage
func Copy(ctx context.Context, storage driver.Driver, srcPath, dstDirPath string, lazyCache ...bool) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	srcPath = utils.FixAndCleanPath(srcPath)
	dstDirPath = utils.FixAndCleanPath(dstDirPath)
	if dstDirPath == stdpath.Dir(srcPath) {
		return stderrors.New("copy in place")
	}
	srcRawObj, err := Get(ctx, storage, srcPath)
	if err != nil {
		return errors.WithMessage(err, "failed to get src object")
	}
	srcObj := model.UnwrapObj(srcRawObj)
	dstDir, err := GetUnwrap(ctx, storage, dstDirPath)
	if err != nil {
		return errors.WithMessage(err, "failed to get dst dir")
	}

	switch s := storage.(type) {
	case driver.CopyResult:
		var newObj model.Obj
		newObj, err = s.Copy(ctx, srcObj, dstDir)
		if err == nil {
			if newObj != nil {
				Cache.addDirectoryObject(storage, dstDirPath, model.WrapObjName(newObj))
			} else if !utils.IsBool(lazyCache...) {
				Cache.DeleteDirectory(storage, dstDirPath)
			}
		}
	case driver.Copy:
		err = s.Copy(ctx, srcObj, dstDir)
		if err == nil {
			if !utils.IsBool(lazyCache...) {
				Cache.DeleteDirectory(storage, dstDirPath)
			}
		}
	default:
		return errs.NotImplement
	}
	return errors.WithStack(err)
}

func Remove(ctx context.Context, storage driver.Driver, path string) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	if utils.PathEqual(path, "/") {
		return errors.New("delete root folder is not allowed, please goto the manage page to delete the storage instead")
	}
	path = utils.FixAndCleanPath(path)
	rawObj, err := Get(ctx, storage, path)
	if err != nil {
		// if object not found, it's ok
		if errs.IsObjectNotFound(err) {
			log.Debugf("%s have been removed", path)
			return nil
		}
		return errors.WithMessage(err, "failed to get object")
	}
	dirPath := stdpath.Dir(path)

	switch s := storage.(type) {
	case driver.Remove:
		err = s.Remove(ctx, model.UnwrapObj(rawObj))
		if err == nil {
			Cache.removeDirectoryObject(storage, dirPath, rawObj)
		}
	default:
		return errs.NotImplement
	}
	return errors.WithStack(err)
}

func Put(ctx context.Context, storage driver.Driver, dstDirPath string, file model.FileStreamer, up driver.UpdateProgress, lazyCache ...bool) error {
	close := file.Close
	defer func() {
		if err := close(); err != nil {
			log.Errorf("failed to close file streamer, %v", err)
		}
	}()
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	// UrlTree PUT
	if storage.GetStorage().Driver == "UrlTree" {
		var link string
		dstDirPath, link = urlTreeSplitLineFormPath(stdpath.Join(dstDirPath, file.GetName()))
		file = &stream.FileStream{Obj: &model.Object{Name: link}}
	}
	// if file exist and size = 0, delete it
	dstDirPath = utils.FixAndCleanPath(dstDirPath)
	dstPath := stdpath.Join(dstDirPath, file.GetName())
	tempName := file.GetName() + ".openlist_to_delete"
	tempPath := stdpath.Join(dstDirPath, tempName)
	fi, err := GetUnwrap(ctx, storage, dstPath)
	if err == nil {
		if fi.GetSize() == 0 {
			err = Remove(ctx, storage, dstPath)
			if err != nil {
				return errors.WithMessagef(err, "while uploading, failed remove existing file which size = 0")
			}
		} else if storage.Config().NoOverwriteUpload {
			// try to rename old obj
			err = Rename(ctx, storage, dstPath, tempName)
			if err != nil {
				return err
			}
		} else {
			file.SetExist(fi)
		}
	}
	err = MakeDir(ctx, storage, dstDirPath)
	if err != nil {
		return errors.WithMessagef(err, "failed to make dir [%s]", dstDirPath)
	}
	parentDir, err := GetUnwrap(ctx, storage, dstDirPath)
	// this should not happen
	if err != nil {
		return errors.WithMessagef(err, "failed to get dir [%s]", dstDirPath)
	}
	// if up is nil, set a default to prevent panic
	if up == nil {
		up = func(p float64) {}
	}

	// 如果小于0，则通过缓存获取完整大小，可能发生于流式上传
	if file.GetSize() < 0 {
		log.Warnf("file size < 0, try to get full size from cache")
		file.CacheFullAndWriter(nil, nil)
	}
	switch s := storage.(type) {
	case driver.PutResult:
		var newObj model.Obj
		newObj, err = s.Put(ctx, parentDir, file, up)
		if err == nil {
			Cache.linkCache.DeleteKey(Key(storage, dstPath))
			if newObj != nil {
				Cache.addDirectoryObject(storage, dstDirPath, model.WrapObjName(newObj))
			} else if !utils.IsBool(lazyCache...) {
				Cache.DeleteDirectory(storage, dstDirPath)
			}
		}
	case driver.Put:
		err = s.Put(ctx, parentDir, file, up)
		if err == nil {
			Cache.linkCache.DeleteKey(Key(storage, dstPath))
			if !utils.IsBool(lazyCache...) {
				Cache.DeleteDirectory(storage, dstDirPath)
			}
		}
	default:
		return errs.NotImplement
	}
	log.Debugf("put file [%s] done", file.GetName())
	if storage.Config().NoOverwriteUpload && fi != nil && fi.GetSize() > 0 {
		if err != nil {
			// upload failed, recover old obj
			err := Rename(ctx, storage, tempPath, file.GetName())
			if err != nil {
				log.Errorf("failed recover old obj: %+v", err)
			}
		} else {
			// upload success, remove old obj
			err = Remove(ctx, storage, tempPath)
		}
	}
	return errors.WithStack(err)
}

func PutURL(ctx context.Context, storage driver.Driver, dstDirPath, dstName, url string, lazyCache ...bool) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	dstDirPath = utils.FixAndCleanPath(dstDirPath)
	dstPath := stdpath.Join(dstDirPath, dstName)
	_, err := GetUnwrap(ctx, storage, dstPath)
	if err == nil {
		return errors.WithStack(errs.ObjectAlreadyExists)
	}
	err = MakeDir(ctx, storage, dstDirPath)
	if err != nil {
		return errors.WithMessagef(err, "failed to make dir [%s]", dstDirPath)
	}
	dstDir, err := GetUnwrap(ctx, storage, dstDirPath)
	if err != nil {
		return errors.WithMessagef(err, "failed to get dir [%s]", dstDirPath)
	}
	switch s := storage.(type) {
	case driver.PutURLResult:
		var newObj model.Obj
		newObj, err = s.PutURL(ctx, dstDir, dstName, url)
		if err == nil {
			Cache.linkCache.DeleteKey(Key(storage, dstPath))
			if newObj != nil {
				Cache.addDirectoryObject(storage, dstDirPath, model.WrapObjName(newObj))
			} else if !utils.IsBool(lazyCache...) {
				Cache.DeleteDirectory(storage, dstDirPath)
			}
		}
	case driver.PutURL:
		err = s.PutURL(ctx, dstDir, dstName, url)
		if err == nil {
			Cache.linkCache.DeleteKey(Key(storage, dstPath))
			if !utils.IsBool(lazyCache...) {
				Cache.DeleteDirectory(storage, dstDirPath)
			}
		}
	default:
		return errors.WithStack(errs.NotImplement)
	}
	log.Debugf("put url [%s](%s) done", dstName, url)
	return errors.WithStack(err)
}

func GetDirectUploadTools(storage driver.Driver) []string {
	du, ok := storage.(driver.DirectUploader)
	if !ok {
		return nil
	}
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return nil
	}
	return du.GetDirectUploadTools()
}

func GetDirectUploadInfo(ctx context.Context, tool string, storage driver.Driver, dstDirPath, dstName string, fileSize int64) (any, error) {
	du, ok := storage.(driver.DirectUploader)
	if !ok {
		return nil, errors.WithStack(errs.NotImplement)
	}
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return nil, errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	dstDirPath = utils.FixAndCleanPath(dstDirPath)
	dstPath := stdpath.Join(dstDirPath, dstName)
	_, err := GetUnwrap(ctx, storage, dstPath)
	if err == nil {
		return nil, errors.WithStack(errs.ObjectAlreadyExists)
	}
	err = MakeDir(ctx, storage, dstDirPath)
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to make dir [%s]", dstDirPath)
	}
	dstDir, err := GetUnwrap(ctx, storage, dstDirPath)
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to get dir [%s]", dstDirPath)
	}
	info, err := du.GetDirectUploadInfo(ctx, tool, dstDir, dstName, fileSize)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return info, nil
}
