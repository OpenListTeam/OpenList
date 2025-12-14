package op

import (
	"context"
	stdpath "path"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

var listG singleflight.Group[[]model.Obj]

// List files in storage, not contains virtual file
func List(ctx context.Context, storage driver.Driver, path string, args model.ListArgs) ([]model.Obj, error) {
	return list(ctx, storage, path, args, nil)
}

func list(ctx context.Context, storage driver.Driver, path string, args model.ListArgs, resultValidator func([]model.Obj) error) ([]model.Obj, error) {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return nil, errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	path = utils.FixAndCleanPath(path)
	log.Debugf("op.List %s", path)
	key := Key(storage, path)
	if !args.Refresh {
		if dirCache, exists := Cache.dirCache.Get(key); exists {
			log.Debugf("use cache when list %s", path)
			objs := dirCache.GetSortedObjects(storage)
			if resultValidator != nil {
				if err := resultValidator(objs); err == nil {
					return objs, nil
				}
			} else {
				return objs, nil
			}
		}
	}

	objs, err, _ := listG.Do(key, func() ([]model.Obj, error) {
		dir, err := GetUnwrap(ctx, storage, path)
		if err != nil {
			return nil, errors.WithMessage(err, "failed get dir")
		}
		log.Debugf("list dir: %+v", dir)
		if !dir.IsDir() {
			return nil, errors.WithStack(errs.NotFolder)
		}
		files, err := storage.List(ctx, dir, args)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list objs")
		}
		// warp obj name
		wrapObjsName(storage, files)
		// sort objs
		if storage.Config().LocalSort {
			model.SortFiles(files, storage.GetStorage().OrderBy, storage.GetStorage().OrderDirection)
		}
		model.ExtractFolder(files, storage.GetStorage().ExtractFolder)

		if !args.SkipHook {
			// call hooks
			go func(reqPath string, files []model.Obj) {
				HandleObjsUpdateHook(context.WithoutCancel(ctx), reqPath, files)
			}(utils.GetFullPath(storage.GetStorage().MountPath, path), files)
		}

		if !storage.Config().NoCache {
			if len(files) > 0 {
				log.Debugf("set cache: %s => %+v", key, files)

				ttl := storage.GetStorage().CacheExpiration

				customCachePolicies := storage.GetStorage().CustomCachePolicies
				if len(customCachePolicies) > 0 {
					configPolicies := strings.Split(customCachePolicies, "\n")
					for _, configPolicy := range configPolicies {
						pattern, ttlstr, ok := strings.Cut(strings.TrimSpace(configPolicy), ":")
						if !ok {
							log.Warnf("Malformed custom cache policy entry: %s in storage %s for path %s. Expected format: pattern:ttl", configPolicy, storage.GetStorage().MountPath, path)
							continue
						}
						if match, err1 := doublestar.Match(pattern, path); err1 != nil {
							log.Warnf("Invalid glob pattern in custom cache policy: %s, error: %v", pattern, err1)
							continue
						} else if !match {
							continue
						}

						if configTtl, err1 := strconv.ParseInt(ttlstr, 10, 64); err1 == nil {
							ttl = int(configTtl)
							break
						}
					}
				}

				duration := time.Minute * time.Duration(ttl)
				Cache.dirCache.SetWithTTL(key, newDirectoryCache(files), duration)
			} else {
				log.Debugf("del cache: %s", key)
				Cache.deleteDirectoryTree(key)
			}
		}
		return files, nil
	})
	if err != nil {
		return nil, err
	}
	if resultValidator != nil {
		if err := resultValidator(objs); err != nil {
			return nil, err
		}
	}
	return objs, nil
}

// Get object from list of files
func Get(ctx context.Context, storage driver.Driver, path string, excludeTempObj ...bool) (model.Obj, error) {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return nil, errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	path = utils.FixAndCleanPath(path)
	log.Debugf("op.Get %s", path)

	// is root folder
	if path == "/" {
		if getRooter, ok := storage.(driver.GetRooter); ok {
			rootObj, err := getRooter.GetRoot(ctx)
			if err != nil {
				return nil, errors.WithMessage(err, "failed get root obj")
			}
			return rootObj, nil
		}
		switch r := storage.(type) {
		case driver.IRootId:
			return &model.Object{
				ID:       r.GetRootId(),
				Name:     RootName,
				Modified: storage.GetStorage().Modified,
				IsFolder: true,
				Mask:     model.Locked,
			}, nil
		case driver.IRootPath:
			return &model.Object{
				Path:     r.GetRootPath(),
				Name:     RootName,
				Modified: storage.GetStorage().Modified,
				Mask:     model.Locked,
				IsFolder: true,
			}, nil
		}
		return nil, errors.New("please implement GetRooter or IRootPath or IRootId interface")
	}

	// try get from cache first
	dir, name := stdpath.Split(path)
	dirCache, dirCacheExists := Cache.dirCache.Get(Key(storage, dir))
	refreshList := false
	excludeTemp := utils.IsBool(excludeTempObj...)
	if dirCacheExists {
		files := dirCache.GetSortedObjects(storage)
		for _, f := range files {
			if f.GetName() == name {
				if excludeTemp && model.ObjHasMask(f, model.Temp) {
					refreshList = true
					break
				}
				return f, nil
			}
		}
	}

	// get the obj directly without list so that we can reduce the io
	if g, ok := storage.(driver.Getter); ok {
		obj, err := g.Get(ctx, path)
		if err == nil {
			return obj, nil
		}
		if !errs.IsNotImplementError(err) && !errs.IsNotSupportError(err) {
			return nil, errors.WithMessage(err, "failed to get obj")
		}
	}

	if !dirCacheExists || refreshList {
		var obj model.Obj
		list(ctx, storage, dir, model.ListArgs{Refresh: refreshList}, func(objs []model.Obj) error {
			for _, f := range objs {
				if f.GetName() == name {
					if excludeTemp && model.ObjHasMask(f, model.Temp) {
						return errs.ObjectNotFound
					}
					obj = f
					return nil
				}
			}
			return nil
		})
		if obj != nil {
			return obj, nil
		}
	}
	log.Debugf("cant find obj with name: %s", name)
	return nil, errors.WithStack(errs.ObjectNotFound)
}

func GetUnwrap(ctx context.Context, storage driver.Driver, path string) (model.Obj, error) {
	obj, err := Get(ctx, storage, path, true)
	if err != nil {
		return nil, err
	}
	return model.UnwrapObjName(obj), err
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
	if mode&driver.LinkCacheIP != 0 {
		typeKey += "/" + args.IP
	}
	if mode&driver.LinkCacheUA != 0 {
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
	for {
		ol, err, _ := linkG.Do(key+"/"+typeKey, fn)
		if err != nil {
			return nil, nil, err
		}
		if ol.link.SyncClosers.AcquireReference() || !ol.link.RequireReference {
			return ol.link, ol.obj, nil
		}
	}
}

// Other api
func Other(ctx context.Context, storage driver.Driver, args model.FsOtherArgs) (any, error) {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return nil, errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	o, ok := storage.(driver.Other)
	if !ok {
		return nil, errs.NotImplement
	}
	obj, err := GetUnwrap(ctx, storage, args.Path)
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to get obj")
	}
	return o.Other(ctx, model.OtherArgs{
		Obj:    obj,
		Method: args.Method,
		Data:   args.Data,
	})
}

var mkdirG singleflight.Group[any]

func MakeDir(ctx context.Context, storage driver.Driver, path string) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	path = utils.FixAndCleanPath(path)
	key := Key(storage, path)
	_, err, _ := mkdirG.Do(key, func() (any, error) {
		// check if dir exists
		f, err := Get(ctx, storage, path)
		if err == nil {
			if f.IsDir() {
				return nil, nil
			}
			return nil, errors.New("file exists")
		}
		if !errs.IsObjectNotFound(err) {
			return nil, errors.WithMessage(err, "failed to check if dir exists")
		}
		parentPath, dirName := stdpath.Split(path)
		if err = MakeDir(ctx, storage, parentPath); err != nil {
			return nil, errors.WithMessagef(err, "failed to make parent dir [%s]", parentPath)
		}
		parentDir, err := GetUnwrap(ctx, storage, parentPath)
		// this should not happen
		if err != nil {
			return nil, errors.WithMessagef(err, "failed to get parent dir [%s]", parentPath)
		}
		if model.ObjHasMask(parentDir, model.NoWrite) {
			return nil, errors.WithStack(errs.PermissionDenied)
		}

		var newObj model.Obj
		switch s := storage.(type) {
		case driver.MkdirResult:
			newObj, err = s.MakeDir(ctx, parentDir, dirName)
		case driver.Mkdir:
			err = s.MakeDir(ctx, parentDir, dirName)
		default:
			return nil, errs.NotImplement
		}
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if storage.Config().NoCache {
			return nil, nil
		}
		if dirCache, exist := Cache.dirCache.Get(Key(storage, parentPath)); exist {
			if newObj == nil {
				t := time.Now()
				newObj = &model.Object{
					Name:     dirName,
					IsFolder: true,
					Modified: t,
					Ctime:    t,
					Mask:     model.Temp,
				}
			}
			dirCache.UpdateObject("", wrapObjName(storage, newObj))
		}
		return nil, nil
	})
	return err
}

func Move(ctx context.Context, storage driver.Driver, srcPath, dstDirPath string) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	srcPath = utils.FixAndCleanPath(srcPath)
	if utils.PathEqual(srcPath, "/") {
		return errors.New("move root folder is not allowed")
	}
	srcDirPath := stdpath.Dir(srcPath)
	dstDirPath = utils.FixAndCleanPath(dstDirPath)
	if dstDirPath == srcDirPath {
		return errors.New("move in place")
	}
	srcRawObj, err := Get(ctx, storage, srcPath, true)
	if err != nil {
		return errors.WithMessage(err, "failed to get src object")
	}
	if model.ObjHasMask(srcRawObj, model.NoMove) {
		return errors.WithStack(errs.PermissionDenied)
	}
	srcObj := model.UnwrapObjName(srcRawObj)
	dstDir, err := GetUnwrap(ctx, storage, dstDirPath)
	if err != nil {
		return errors.WithMessage(err, "failed to get dst dir")
	}
	if model.ObjHasMask(dstDir, model.NoWrite) {
		return errors.WithStack(errs.PermissionDenied)
	}

	var newObj model.Obj
	switch s := storage.(type) {
	case driver.MoveResult:
		newObj, err = s.Move(ctx, srcObj, dstDir)
	case driver.Move:
		err = s.Move(ctx, srcObj, dstDir)
	default:
		err = errs.NotImplement
	}
	if err != nil {
		return errors.WithStack(err)
	}

	srcKey := Key(storage, srcDirPath)
	dstKey := Key(storage, dstDirPath)
	if !srcRawObj.IsDir() {
		Cache.linkCache.DeleteKey(stdpath.Join(srcKey, srcRawObj.GetName()))
		Cache.linkCache.DeleteKey(stdpath.Join(dstKey, srcRawObj.GetName()))
	}
	if !storage.Config().NoCache {
		if cache, exist := Cache.dirCache.Get(srcKey); exist {
			if srcRawObj.IsDir() {
				Cache.deleteDirectoryTree(stdpath.Join(srcKey, srcRawObj.GetName()))
			}
			cache.RemoveObject(srcRawObj.GetName())
		}
		if cache, exist := Cache.dirCache.Get(dstKey); exist {
			if newObj == nil {
				newObj = &model.ObjWrapMask{Obj: srcRawObj, Mask: model.Temp}
			} else {
				newObj = wrapObjName(storage, newObj)
			}
			cache.UpdateObject(srcRawObj.GetName(), newObj)
		}
	}

	if ctx.Value(conf.SkipHookKey) != nil || !needHandleObjsUpdateHook() {
		return nil
	}
	if !srcObj.IsDir() {
		go objsUpdateHook(context.WithoutCancel(ctx), storage, dstDirPath, false)
	} else {
		go objsUpdateHook(context.WithoutCancel(ctx), storage, stdpath.Join(dstDirPath, srcObj.GetName()), true)
	}
	return nil
}

func Rename(ctx context.Context, storage driver.Driver, srcPath, dstName string) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	srcPath = utils.FixAndCleanPath(srcPath)
	if utils.PathEqual(srcPath, "/") {
		return errors.New("rename root folder is not allowed")
	}
	srcRawObj, err := Get(ctx, storage, srcPath, true)
	if err != nil {
		return errors.WithMessage(err, "failed to get src object")
	}
	if model.ObjHasMask(srcRawObj, model.NoRename) {
		return errors.WithStack(errs.PermissionDenied)
	}
	srcObj := model.UnwrapObjName(srcRawObj)

	var newObj model.Obj
	switch s := storage.(type) {
	case driver.RenameResult:
		newObj, err = s.Rename(ctx, srcObj, dstName)
	case driver.Rename:
		err = s.Rename(ctx, srcObj, dstName)
	default:
		return errs.NotImplement
	}
	if err != nil {
		return errors.WithStack(err)
	}

	dirKey := Key(storage, stdpath.Dir(srcPath))
	if !srcRawObj.IsDir() {
		Cache.linkCache.DeleteKey(stdpath.Join(dirKey, srcRawObj.GetName()))
		Cache.linkCache.DeleteKey(stdpath.Join(dirKey, dstName))
	}
	if !storage.Config().NoCache {
		if cache, exist := Cache.dirCache.Get(dirKey); exist {
			if srcRawObj.IsDir() {
				Cache.deleteDirectoryTree(stdpath.Join(dirKey, srcRawObj.GetName()))
			}
			if newObj == nil {
				newObj = &model.ObjWrapMask{Obj: &model.ObjWrapName{Name: dstName, Obj: srcObj}, Mask: model.Temp}
			}
			newObj = wrapObjName(storage, newObj)
			cache.UpdateObject(srcRawObj.GetName(), newObj)
		}
	}

	if ctx.Value(conf.SkipHookKey) != nil || !needHandleObjsUpdateHook() {
		return nil
	}
	dstDirPath := stdpath.Dir(srcPath)
	if !srcObj.IsDir() {
		go objsUpdateHook(context.WithoutCancel(ctx), storage, dstDirPath, false)
	} else {
		go objsUpdateHook(context.WithoutCancel(ctx), storage, stdpath.Join(dstDirPath, srcObj.GetName()), true)
	}
	return nil
}

// Copy Just copy file[s] in a storage
func Copy(ctx context.Context, storage driver.Driver, srcPath, dstDirPath string) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	srcPath = utils.FixAndCleanPath(srcPath)
	dstDirPath = utils.FixAndCleanPath(dstDirPath)
	if dstDirPath == stdpath.Dir(srcPath) {
		return errors.New("copy in place")
	}
	srcRawObj, err := Get(ctx, storage, srcPath, true)
	if err != nil {
		return errors.WithMessage(err, "failed to get src object")
	}
	// if model.ObjHasMask(srcRawObj, model.NoCopy) {
	// 	return errors.WithStack(errs.PermissionDenied)
	// }
	srcObj := model.UnwrapObjName(srcRawObj)
	dstDir, err := GetUnwrap(ctx, storage, dstDirPath)
	if err != nil {
		return errors.WithMessage(err, "failed to get dst dir")
	}
	if model.ObjHasMask(dstDir, model.NoWrite) {
		return errors.WithStack(errs.PermissionDenied)
	}

	var newObj model.Obj
	switch s := storage.(type) {
	case driver.CopyResult:
		newObj, err = s.Copy(ctx, srcObj, dstDir)
	case driver.Copy:
		err = s.Copy(ctx, srcObj, dstDir)
	default:
		err = errs.NotImplement
	}
	if err != nil {
		return errors.WithStack(err)
	}

	dstKey := Key(storage, dstDirPath)
	if !srcRawObj.IsDir() {
		Cache.linkCache.DeleteKey(stdpath.Join(dstKey, srcRawObj.GetName()))
	}
	if !storage.Config().NoCache {
		if cache, exist := Cache.dirCache.Get(dstKey); exist {
			if newObj == nil {
				newObj = &model.ObjWrapMask{Obj: srcRawObj, Mask: model.Temp}
			} else {
				newObj = wrapObjName(storage, newObj)
			}
			cache.UpdateObject(srcRawObj.GetName(), newObj)
		}
	}

	if ctx.Value(conf.SkipHookKey) != nil || !needHandleObjsUpdateHook() {
		return nil
	}
	if !srcObj.IsDir() {
		go objsUpdateHook(context.WithoutCancel(ctx), storage, dstDirPath, false)
	} else {
		go objsUpdateHook(context.WithoutCancel(ctx), storage, stdpath.Join(dstDirPath, srcObj.GetName()), true)
	}
	return nil
}

func Remove(ctx context.Context, storage driver.Driver, path string) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	path = utils.FixAndCleanPath(path)
	if utils.PathEqual(path, "/") {
		return errors.New("delete root folder is not allowed")
	}
	rawObj, err := Get(ctx, storage, path, true)
	if err != nil {
		// if object not found, it's ok
		if errs.IsObjectNotFound(err) {
			log.Debugf("%s have been removed", path)
			return nil
		}
		return errors.WithMessage(err, "failed to get object")
	}
	if model.ObjHasMask(rawObj, model.NoRemove) {
		return errors.WithStack(errs.PermissionDenied)
	}
	dirPath := stdpath.Dir(path)

	switch s := storage.(type) {
	case driver.Remove:
		err = s.Remove(ctx, model.UnwrapObjName(rawObj))
		if err == nil {
			Cache.removeDirectoryObject(storage, dirPath, rawObj)
		}
	default:
		return errs.NotImplement
	}
	return errors.WithStack(err)
}

func Put(ctx context.Context, storage driver.Driver, dstDirPath string, file model.FileStreamer, up driver.UpdateProgress) error {
	defer func() {
		if err := file.Close(); err != nil {
			log.Errorf("failed to close file streamer, %v", err)
		}
	}()
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	// UrlTree PUT
	if storage.Config().OnlyIndices {
		var link string
		dstDirPath, link = urlTreeSplitLineFormPath(stdpath.Join(dstDirPath, file.GetName()))
		file = &stream.FileStream{Obj: &model.Object{Name: link}, Closers: utils.Closers{file}}
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
	if model.ObjHasMask(parentDir, model.NoWrite) {
		return errors.WithStack(errs.PermissionDenied)
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

	var newObj model.Obj
	switch s := storage.(type) {
	case driver.PutResult:
		newObj, err = s.Put(ctx, parentDir, file, up)
	case driver.Put:
		err = s.Put(ctx, parentDir, file, up)
	default:
		return errs.NotImplement
	}
	if err == nil {
		Cache.linkCache.DeleteKey(Key(storage, dstPath))
		if !storage.Config().NoCache {
			if cache, exist := Cache.dirCache.Get(Key(storage, dstDirPath)); exist {
				if newObj == nil {
					newObj = &model.Object{
						Name:     file.GetName(),
						Size:     file.GetSize(),
						Modified: file.ModTime(),
						Ctime:    file.CreateTime(),
						Mask:     model.Temp,
					}
				}
				newObj = wrapObjName(storage, newObj)
				cache.UpdateObject(newObj.GetName(), newObj)
			}
		}

		if ctx.Value(conf.SkipHookKey) == nil && needHandleObjsUpdateHook() {
			go objsUpdateHook(context.WithoutCancel(ctx), storage, dstDirPath, false)
		}
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

func PutURL(ctx context.Context, storage driver.Driver, dstDirPath, dstName, url string) error {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	dstDirPath = utils.FixAndCleanPath(dstDirPath)
	dstPath := stdpath.Join(dstDirPath, dstName)

	if _, err := Get(ctx, storage, dstPath); err == nil {
		return errors.WithStack(errs.ObjectAlreadyExists)
	}
	err := MakeDir(ctx, storage, dstDirPath)
	if err != nil {
		return errors.WithMessagef(err, "failed to make dir [%s]", dstDirPath)
	}
	dstDir, err := GetUnwrap(ctx, storage, dstDirPath)
	if err != nil {
		return errors.WithMessagef(err, "failed to get dir [%s]", dstDirPath)
	}
	if model.ObjHasMask(dstDir, model.NoWrite) {
		return errors.WithStack(errs.PermissionDenied)
	}
	var newObj model.Obj
	switch s := storage.(type) {
	case driver.PutURLResult:
		newObj, err = s.PutURL(ctx, dstDir, dstName, url)
	case driver.PutURL:
		err = s.PutURL(ctx, dstDir, dstName, url)
	default:
		return errors.WithStack(errs.NotImplement)
	}
	if err == nil {
		Cache.linkCache.DeleteKey(Key(storage, dstPath))
		if !storage.Config().NoCache {
			if cache, exist := Cache.dirCache.Get(Key(storage, dstDirPath)); exist {
				if newObj == nil {
					t := time.Now()
					newObj = &model.Object{
						Name:     dstName,
						Modified: t,
						Ctime:    t,
						Mask:     model.Temp,
					}
				}
				newObj = wrapObjName(storage, newObj)
				cache.UpdateObject(newObj.GetName(), newObj)
			}

			if ctx.Value(conf.SkipHookKey) == nil && needHandleObjsUpdateHook() {
				go objsUpdateHook(context.WithoutCancel(ctx), storage, dstDirPath, false)
			}
		}
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
	_, err := Get(ctx, storage, dstPath)
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

func objsUpdateHook(ctx context.Context, storage driver.Driver, dirPath string, recursive bool) {
	files, err := List(ctx, storage, dirPath, model.ListArgs{SkipHook: true})
	if err != nil {
		return
	}
	if !recursive {
		HandleObjsUpdateHook(ctx, utils.GetFullPath(storage.GetStorage().MountPath, dirPath), files)
		return
	}
	var limiter *rate.Limiter
	if l, _ := GetSettingItemByKey(conf.HandleHookRateLimit); l != nil {
		if f, e := strconv.ParseFloat(l.Value, 64); e == nil && f > .0 {
			limiter = rate.NewLimiter(rate.Limit(f), 1)
		}
	}
	recursivelyObjsUpdateHook(ctx, storage, dirPath, files, limiter)
}
func recursivelyObjsUpdateHook(ctx context.Context, storage driver.Driver, dirPath string, files []model.Obj, limiter *rate.Limiter) {
	HandleObjsUpdateHook(ctx, utils.GetFullPath(storage.GetStorage().MountPath, dirPath), files)
	for _, f := range files {
		if utils.IsCanceled(ctx) {
			return
		}
		if !f.IsDir() {
			continue
		}
		dstPath := stdpath.Join(dirPath, f.GetName())
		if limiter != nil {
			if err := limiter.Wait(ctx); err != nil {
				return
			}
		}
		files, err := List(ctx, storage, dstPath, model.ListArgs{SkipHook: true})
		if err == nil {
			recursivelyObjsUpdateHook(ctx, storage, dstPath, files, limiter)
		}
	}
}

func needHandleObjsUpdateHook() bool {
	if len(objsUpdateHooks) < 1 {
		return false
	}
	needHandle, _ := GetSettingItemByKey(conf.HandleHookAfterWriting)
	return needHandle != nil && (needHandle.Value == "true" || needHandle.Value == "1")
}

func wrapObjsName(storage driver.Driver, objs []model.Obj) {
	if _, ok := storage.(driver.Getter); !ok {
		model.WrapObjsName(objs)
	}
}
func wrapObjName(storage driver.Driver, obj model.Obj) model.Obj {
	if _, ok := storage.(driver.Getter); !ok {
		return model.WrapObjName(obj)
	}
	return obj
}
