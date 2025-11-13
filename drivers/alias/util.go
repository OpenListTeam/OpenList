package alias

import (
	"context"
	"errors"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	log "github.com/sirupsen/logrus"
)

type detailWithIndex struct {
	idx int
	val *model.StorageDetails
}

func (d *Alias) listRoot(ctx context.Context, withDetails, refresh bool) []model.Obj {
	var objs []model.Obj
	detailsChan := make(chan detailWithIndex, len(d.pathMap))
	workerCount := 0
	for _, k := range d.rootOrder {
		obj := model.Object{
			Name:     k,
			IsFolder: true,
			Modified: d.Modified,
		}
		idx := len(objs)
		objs = append(objs, &obj)
		v := d.pathMap[k]
		if !withDetails || len(v) != 1 {
			continue
		}
		remoteDriver, err := op.GetStorageByMountPath(v[0])
		if err != nil {
			continue
		}
		_, ok := remoteDriver.(driver.WithDetails)
		if !ok {
			continue
		}
		objs[idx] = &model.ObjStorageDetails{
			Obj: objs[idx],
			StorageDetailsWithName: model.StorageDetailsWithName{
				StorageDetails: nil,
				DriverName:     remoteDriver.Config().Name,
			},
		}
		workerCount++
		go func(dri driver.Driver, i int) {
			details, e := op.GetStorageDetails(ctx, dri, refresh)
			if e != nil {
				if !errors.Is(e, errs.NotImplement) && !errors.Is(e, errs.StorageNotInit) {
					log.Errorf("failed get %s storage details: %+v", dri.GetStorage().MountPath, e)
				}
			}
			detailsChan <- detailWithIndex{idx: i, val: details}
		}(remoteDriver, idx)
	}
	for workerCount > 0 {
		select {
		case r := <-detailsChan:
			objs[r.idx].(*model.ObjStorageDetails).StorageDetails = r.val
			workerCount--
		case <-time.After(time.Second):
			workerCount = 0
		}
	}
	return objs
}

// do others that not defined in Driver interface
func getPair(path string) (string, string) {
	// path = strings.TrimSpace(path)
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

func (d *Alias) link(ctx context.Context, reqPath string, args model.LinkArgs) (*model.Link, model.Obj, error) {
	storage, reqActualPath, err := op.GetStorageAndActualPath(reqPath)
	if err != nil {
		return nil, nil, err
	}
	if !args.Redirect {
		return op.Link(ctx, storage, reqActualPath, args)
	}
	obj, err := fs.Get(ctx, reqPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		return nil, nil, err
	}
	if common.ShouldProxy(storage, stdpath.Base(reqPath)) {
		return nil, obj, nil
	}
	return op.Link(ctx, storage, reqActualPath, args)
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

func (d *Alias) extract(ctx context.Context, reqPath string, args model.ArchiveInnerArgs) (*model.Link, error) {
	storage, reqActualPath, err := op.GetStorageAndActualPath(reqPath)
	if err != nil {
		return nil, err
	}
	if _, ok := storage.(driver.ArchiveReader); !ok {
		return nil, errs.NotImplement
	}
	if args.Redirect && common.ShouldProxy(storage, stdpath.Base(reqPath)) {
		_, err := fs.Get(ctx, reqPath, &fs.GetArgs{NoLog: true})
		if err == nil {
			return nil, err
		}
		return nil, nil
	}
	link, _, err := op.DriverExtract(ctx, storage, reqActualPath, args)
	return link, err
}
