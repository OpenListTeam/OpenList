package task_group

import (
	"context"
	"fmt"
	"path"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type SrcPathToRemove string

// ActualPath
type DstPathToRefresh string

func RefreshAndRemove(dstPath string, payloads ...any) {
	dstStorage, dstActualPath, err := op.GetStorageAndActualPath(dstPath)
	if err != nil {
		log.Error(errors.WithMessage(err, "failed get dst storage"))
		return
	}
	_, dstNeedRefresh := dstStorage.(driver.Put)
	dstNeedRefresh = dstNeedRefresh && !dstStorage.Config().NoCache
	if dstNeedRefresh {
		op.DeleteCache(dstStorage, dstActualPath)
	}
	var ctx context.Context
	for _, payload := range payloads {
		switch p := payload.(type) {
		case DstPathToRefresh:
			if dstNeedRefresh {
				op.DeleteCache(dstStorage, string(p))
			}
		case SrcPathToRemove:
			if ctx == nil {
				ctx = context.Background()
			}
			srcStorage, srcActualPath, err := op.GetStorageAndActualPath(string(p))
			if err != nil {
				log.Error(errors.WithMessage(err, "failed get src storage"))
				continue
			}
			err = verifyAndRemove(ctx, srcStorage, dstStorage, srcActualPath, dstActualPath, dstNeedRefresh)
			if err != nil {
				log.Error(err)
			}
		}
	}
}

func verifyAndRemove(ctx context.Context, srcStorage, dstStorage driver.Driver, srcPath, dstPath string, refresh bool) error {
	srcObj, err := op.Get(ctx, srcStorage, srcPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] file", path.Join(srcStorage.GetStorage().MountPath, srcPath))
	}

	dstObjPath := path.Join(dstPath, srcObj.GetName())
	dstObj, err := op.Get(ctx, dstStorage, dstObjPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get dst [%s] file", path.Join(dstStorage.GetStorage().MountPath, dstObjPath))
	}

	if !dstObj.IsDir() {
		err = op.Remove(ctx, srcStorage, srcPath)
		if err != nil {
			return fmt.Errorf("failed remove %s: %+v", path.Join(srcStorage.GetStorage().MountPath, srcPath), err)
		}
		return nil
	}

	// Verify directory
	srcObjs, err := op.List(ctx, srcStorage, srcPath, model.ListArgs{})
	if err != nil {
		return errors.WithMessagef(err, "failed list src [%s] objs", path.Join(srcStorage.GetStorage().MountPath, srcPath))
	}

	if refresh {
		op.DeleteCache(dstStorage, dstObjPath)
	}
	hasErr := false
	for _, obj := range srcObjs {
		srcSubPath := path.Join(srcPath, obj.GetName())
		err := verifyAndRemove(ctx, srcStorage, dstStorage, srcSubPath, dstObjPath, refresh)
		if err != nil {
			log.Error(err)
			hasErr = true
		}
	}
	if hasErr {
		return errors.Errorf("some subitems of [%s] failed to verify and remove", path.Join(srcStorage.GetStorage().MountPath, srcPath))
	}
	err = op.Remove(ctx, srcStorage, srcPath)
	if err != nil {
		return fmt.Errorf("failed remove %s: %+v", path.Join(srcStorage.GetStorage().MountPath, srcPath), err)
	}
	return nil
}

var TransferCoordinator *TaskGroupCoordinator = NewTaskGroupCoordinator("RefreshAndRemove", RefreshAndRemove)
