package op

import (
	"context"
	stdpath "path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
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
		// warp obj name
		wrapObjsName(storage, files)
		// sort objs
		if storage.Config().LocalSort {
			model.SortFiles(files, storage.GetStorage().OrderBy, storage.GetStorage().OrderDirection)
		}
		model.ExtractFolder(files, storage.GetStorage().ExtractFolder)
		// call hooks
		go func(reqPath string, files []model.Obj) {
			HandleObjsUpdateHook(context.WithoutCancel(ctx), reqPath, files)
		}(utils.GetFullPath(storage.GetStorage().MountPath, path), files)

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
func Get(ctx context.Context, storage driver.Driver, path string, noTempObj ...bool) (model.Obj, error) {
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
		switch r := storage.GetAddition().(type) {
		case driver.IRootId:
			return &model.Object{
				ID:       r.GetRootId(),
				Name:     RootName,
				Modified: storage.GetStorage().Modified,
				IsFolder: true,
			}, nil
		case driver.IRootPath:
			return &model.Object{
				Path:     r.GetRootPath(),
				Name:     RootName,
				Modified: storage.GetStorage().Modified,
				IsFolder: true,
			}, nil
		}
		return nil, errors.New("please implement GetRooter or IRootPath or IRootId interface")
	}

	// try get from cache first
	dir, name := stdpath.Split(path)
	dirCache, dirCacheExists := Cache.dirCache.Get(Key(storage, dir))
	refreshList := false
	noTemp := utils.IsBool(noTempObj...)
	if dirCacheExists {
		files := dirCache.GetSortedObjects(storage)
		name := stdpath.Base(path)
		for _, f := range files {
			if f.GetName() == name {
				if noTemp && model.ObjHasMask(f, model.Temp) {
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
		_, err := list(ctx, storage, dir, model.ListArgs{Refresh: refreshList}, func(objs []model.Obj) error {
			for _, f := range objs {
				if f.GetName() == name {
					if noTemp && model.ObjHasMask(f, model.Temp) {
						break
					}
					obj = f
					return nil
				}
			}
			return errors.WithStack(errs.ObjectNotFound)
		})
		if err != nil {
			log.Debugf("cant find obj with name: %s", name)
			return nil, err
		}
		return obj, nil
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
			if !storage.Config().NoCache {
				if dirCache, exist := Cache.dirCache.Get(Key(storage, parentPath)); exist {
					if newObj == nil {
						t := time.Now()
						newObj = model.ObjAddMask(&model.Object{
							Name:     dirName,
							IsFolder: true,
							Modified: t,
							Ctime:    t,
						}, model.Temp)
					}
					dirCache.UpdateObject("", wrapObjName(storage, newObj))
				}
			}
		}
		return nil, errors.WithMessage(err, "failed to check if dir exists")
	})
	return err
}

func Move(ctx context.Context, storage driver.Driver, srcPath, dstDirPath string, lazyCache ...bool) error {
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
	srcObj := model.UnwrapObjName(srcRawObj)
	dstDir, err := GetUnwrap(ctx, storage, dstDirPath)
	if err != nil {
		return errors.WithMessage(err, "failed to get dst dir")
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
	if err == nil {
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
					newObj = model.ObjAddMask(srcObj, model.Temp)
				}
				cache.UpdateObject(srcRawObj.GetName(), wrapObjName(storage, newObj))
			}
		}
	}

	if !utils.IsBool(lazyCache...) && err == nil && needHandleObjsUpdateHook() {
		if !srcObj.IsDir() {
			go List(context.Background(), storage, dstDirPath, model.ListArgs{Refresh: true})
		} else {
			targetPath := stdpath.Join(dstDirPath, srcObj.GetName())
			var limiter *rate.Limiter
			if l, _ := GetSettingItemByKey(conf.HandleHookRateLimit); l != nil {
				if f, e := strconv.ParseFloat(l.Value, 64); e == nil && f > .0 {
					limiter = rate.NewLimiter(rate.Limit(f), 1)
				}
			}
			go RecursivelyListStorage(context.Background(), storage, targetPath, limiter, nil)
		}
	}

	return errors.WithStack(err)
}

func Rename(ctx context.Context, storage driver.Driver, srcPath, dstName string, lazyCache ...bool) error {
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
	if newObj == nil {
		newObj = model.ObjAddMask(&model.ObjWrapName{Name: dstName, Obj: srcObj}, model.Temp)
	}
	Cache.updateDirectoryObject(storage, stdpath.Dir(srcPath), srcRawObj, wrapObjName(storage, newObj))
	return nil
}

// Copy Just copy file[s] in a storage
func Copy(ctx context.Context, storage driver.Driver, srcPath, dstDirPath string, lazyCache ...bool) error {
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
	srcObj := model.UnwrapObjName(srcRawObj)
	dstDir, err := GetUnwrap(ctx, storage, dstDirPath)
	if err != nil {
		return errors.WithMessage(err, "failed to get dst dir")
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
	if err == nil {
		dstKey := Key(storage, dstDirPath)
		if !srcRawObj.IsDir() {
			Cache.linkCache.DeleteKey(stdpath.Join(dstKey, srcRawObj.GetName()))
		}
		if !storage.Config().NoCache {
			if cache, exist := Cache.dirCache.Get(dstKey); exist {
				if newObj == nil {
					newObj = model.ObjAddMask(srcObj, model.Temp)
				}
				cache.UpdateObject(srcRawObj.GetName(), wrapObjName(storage, newObj))
			}
		}
	}

	if !utils.IsBool(lazyCache...) && err == nil && needHandleObjsUpdateHook() {
		if !srcObj.IsDir() {
			go List(context.Background(), storage, dstDirPath, model.ListArgs{Refresh: true})
		} else {
			targetPath := stdpath.Join(dstDirPath, srcObj.GetName())
			var limiter *rate.Limiter
			if l, _ := GetSettingItemByKey(conf.HandleHookRateLimit); l != nil {
				if f, e := strconv.ParseFloat(l.Value, 64); e == nil && f > .0 {
					limiter = rate.NewLimiter(rate.Limit(f), 1)
				}
			}
			go RecursivelyListStorage(context.Background(), storage, targetPath, limiter, nil)
		}
	}

	return errors.WithStack(err)
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
	if storage.Config().OnlyIndices {
		var link string
		dstDirPath, link = urlTreeSplitLineFormPath(stdpath.Join(dstDirPath, file.GetName()))
		file = &stream.FileStream{Obj: &model.Object{Name: file.GetName(), Path: link}}
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
					newObj = model.ObjAddMask(&model.Object{
						Name:     file.GetName(),
						Size:     file.GetSize(),
						Modified: file.ModTime(),
						Ctime:    file.CreateTime(),
					}, model.Temp)
				}
				cache.UpdateObject(newObj.GetName(), wrapObjName(storage, newObj))
			}
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
	if !utils.IsBool(lazyCache...) && err == nil && needHandleObjsUpdateHook() {
		go List(context.Background(), storage, dstDirPath, model.ListArgs{Refresh: true})
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
	s, ok := storage.(driver.PutURLResult)
	if !ok {
		return errors.WithStack(errs.NotImplement)
	}
	var newObj model.Obj
	newObj, err = s.PutURL(ctx, dstDir, dstName, url)
	if err == nil {
		Cache.linkCache.DeleteKey(Key(storage, dstPath))
		if !storage.Config().NoCache {
			if newObj != nil {
				cache, exist := Cache.dirCache.Get(Key(storage, dstDirPath))
				if exist {
					cache.UpdateObject(newObj.GetName(), wrapObjName(storage, newObj))
				}
			} else {
				err = errors.New("PutURL returned nil obj")
			}
		}
	}

	if err == nil && needHandleObjsUpdateHook() {
		go List(context.Background(), storage, dstDirPath, model.ListArgs{Refresh: true})
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

func needHandleObjsUpdateHook() bool {
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
