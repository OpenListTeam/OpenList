package alias

import (
	"context"
	"math/rand"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/pkg/errors"
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
			Path:     "/" + k,
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

func (d *Alias) getAllReqPath(ctx context.Context, objPath string, isParent bool, ifContinue func(err error) (bool, error)) ([]string, error) {
	root, sub := d.getRootAndPath(objPath)
	if sub == "" && !isParent {
		return nil, errs.NotSupport
	}
	dsts, ok := d.pathMap[root]
	if !ok {
		return nil, errs.ObjectNotFound
	}
	var reqPath []string
	for _, dst := range dsts {
		path := stdpath.Join(dst, sub)
		_, err := fs.Get(ctx, path, &fs.GetArgs{NoLog: true})
		cont, err := ifContinue(err)
		if !cont {
			if err == nil {
				return []string{path}, nil
			} else {
				return nil, err
			}
		}
		if err == nil {
			reqPath = append(reqPath, path)
		}
	}
	if len(reqPath) == 0 {
		return nil, errs.ObjectNotFound
	}
	return reqPath, nil
}

func getWriteAndPutFilterFunc(policy string) func(error) (bool, error) {
	if policy == AllWP {
		return func(err error) (bool, error) {
			return true, err
		}
	}
	all := true
	l := 0
	return func(err error) (bool, error) {
		if err != nil {
			switch policy {
			case AllStrictWP:
				return false, ErrSamePathLeak
			case DeterministicOrAllWP:
				if l >= 2 {
					return false, ErrSamePathLeak
				}
			}
			all = false
		} else {
			switch policy {
			case FirstRWP:
				return true, nil
			case DeterministicWP:
				if l > 0 {
					return false, ErrPathConflict
				}
			case DeterministicOrAllWP:
				if l > 0 && !all {
					return false, ErrSamePathLeak
				}
			}
			l += 1
		}
		return true, err
	}
}

func (d *Alias) getWritePath(ctx context.Context, obj model.Obj, isParent bool) ([]string, error) {
	if d.WriteConflictPolicy == DisabledWP {
		return nil, errs.PermissionDenied
	}
	reqPath, err := d.getAllReqPath(ctx, obj.GetPath(), isParent, getWriteAndPutFilterFunc(d.WriteConflictPolicy))
	if err != nil {
		return nil, err
	}
	return reqPath, nil
}

func (d *Alias) getPutPath(ctx context.Context, obj model.Obj) ([]string, error) {
	if d.PutConflictPolicy == DisabledWP {
		return nil, errs.PermissionDenied
	}
	reqPath, err := d.getAllReqPath(ctx, obj.GetPath(), true, getWriteAndPutFilterFunc(d.PutConflictPolicy))
	if err != nil {
		return nil, err
	}
	if d.PutConflictPolicy == RandomBalancedRP {
		ri := rand.Intn(len(reqPath))
		return []string{reqPath[ri]}, nil
	} else if d.PutConflictPolicy == BalancedByQuotaP || d.PutConflictPolicy == BalancedByQuotaStrictP {
		r, ok := getRandomPathByQuotaBalanced(ctx, reqPath, d.PutConflictPolicy == BalancedByQuotaStrictP, uint64(obj.GetSize()))
		if !ok {
			return nil, ErrNoEnoughSpace
		}
		return []string{r}, nil
	} else {
		return reqPath, nil
	}
}

func getRandomPathByQuotaBalanced(ctx context.Context, reqPath []string, strict bool, objSize uint64) (string, bool) {
	// Get all space
	details := make([]*model.StorageDetails, len(reqPath))
	detailsChan := make(chan detailWithIndex, len(reqPath))
	workerCount := 0
	for i, p := range reqPath {
		s, err := fs.GetStorage(p, &fs.GetStoragesArgs{})
		if err != nil {
			continue
		}
		if _, ok := s.(driver.WithDetails); !ok {
			continue
		}
		workerCount++
		go func(dri driver.Driver, i int) {
			d, e := op.GetStorageDetails(ctx, dri)
			if e != nil {
				if !errors.Is(e, errs.NotImplement) && !errors.Is(e, errs.StorageNotInit) {
					log.Errorf("failed get %s storage details: %+v", dri.GetStorage().MountPath, e)
				}
			}
			detailsChan <- detailWithIndex{idx: i, val: d}
		}(s, i)
	}
	for workerCount > 0 {
		select {
		case r := <-detailsChan:
			details[r.idx] = r.val
			workerCount--
		case <-time.After(time.Second):
			workerCount = 0
		}
	}

	// Try select one that has space info
	selected, ok := selectRandom(details, func(d *model.StorageDetails) uint64 {
		if d == nil || d.FreeSpace < objSize {
			return 0
		}
		return d.FreeSpace
	})
	if !ok {
		if strict {
			return "", false
		} else {
			// No strict mode, return any of non-details ones
			noDetails := make([]int, 0, len(details))
			for i, d := range details {
				if d == nil {
					noDetails = append(noDetails, i)
				}
			}
			if len(noDetails) == 0 {
				return "", false
			}
			return reqPath[noDetails[rand.Intn(len(noDetails))]], true
		}
	}
	return reqPath[selected], true
}

func selectRandom[Item any](arr []Item, getWeight func(Item) uint64) (int, bool) {
	var totalWeight uint64 = 0
	for _, i := range arr {
		totalWeight += getWeight(i)
	}
	if totalWeight == 0 {
		return 0, false
	}
	r := rand.Uint64() % totalWeight
	for i, item := range arr {
		w := getWeight(item)
		if r < w {
			return i, true
		}
		r -= w
	}
	return 0, false
}

func (d *Alias) getCopyMovePath(ctx context.Context, srcObj, dstDir model.Obj) ([]string, []string, error) {
	if d.PutConflictPolicy == DisabledWP {
		return nil, nil, errs.PermissionDenied
	}
	dstPath, err := d.getAllReqPath(ctx, dstDir.GetPath(), true, getWriteAndPutFilterFunc(d.PutConflictPolicy))
	if err != nil {
		return nil, nil, err
	}
	dstStorageMap := make(map[string][]string)
	allocatingDst := make(map[string]struct{})
	for _, dp := range dstPath {
		storage, e := fs.GetStorage(dp, &fs.GetStoragesArgs{})
		if e != nil {
			return nil, nil, errors.WithMessagef(e, "cannot copy or move to virtual path [%s]", dp)
		}
		mp := storage.GetStorage().MountPath
		dstStorageMap[mp] = append(dstStorageMap[mp], dp)
		allocatingDst[dp] = struct{}{}
	}
	srcPath, err := d.getAllReqPath(ctx, srcObj.GetPath(), false, getWriteAndPutFilterFunc(AllWP))
	if err != nil {
		return nil, nil, err
	}
	srcs := make([]string, 0)
	dsts := make([]string, 0)
	for _, sp := range srcPath {
		storage, e := fs.GetStorage(sp, &fs.GetStoragesArgs{})
		if e != nil {
			continue
		}
		if dstPaths, ok := dstStorageMap[storage.GetStorage().MountPath]; ok {
			for _, dp := range dstPaths {
				srcs = append(srcs, sp)
				dsts = append(dsts, dp)
				delete(allocatingDst, dp)
			}
			delete(dstStorageMap, storage.GetStorage().MountPath)
		}
	}
	for dp := range allocatingDst {
		sp := srcs[0]
		if d.ReadConflictPolicy == RandomBalancedRP {
			sp = srcs[rand.Intn(len(srcs))]
		}
		srcs = append(srcs, sp)
		dsts = append(dsts, dp)
	}
	return srcs, dsts, nil
}

func (d *Alias) getArchiveMeta(ctx context.Context, reqPath string, args model.ArchiveArgs) (model.ArchiveMeta, error) {
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

func (d *Alias) listArchive(ctx context.Context, reqPath string, args model.ArchiveInnerArgs) ([]model.Obj, error) {
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
