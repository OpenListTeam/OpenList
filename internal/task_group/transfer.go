package task_group

import (
	"context"
	"fmt"
	"path"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

type SrcPathToRemove string

// ActualPath
type DstPathToHook string

func HookAndRemove(ctx context.Context, dstPath string, payloads ...any) {
	dstStorage, dstActualPath, err := op.GetStorageAndActualPath(dstPath)
	if err != nil {
		log.Error(errors.WithMessage(err, "failed get dst storage"))
		return
	}
	dstNeedHandleHook := setting.GetBool(conf.HandleHookAfterWriting)
	dstHandleHookLimit := setting.GetFloat(conf.HandleHookRateLimit, .0)
	var listLimiter *rate.Limiter
	if dstNeedHandleHook && dstHandleHookLimit > .0 {
		listLimiter = rate.NewLimiter(rate.Limit(dstHandleHookLimit), 1)
	}
	hookedPaths := make(map[string]struct{})
	handleHook := func(actualPath string) {
		if _, ok := hookedPaths[actualPath]; ok {
			return
		}
		if listLimiter != nil {
			_ = listLimiter.Wait(ctx)
		}
		files, e := op.List(ctx, dstStorage, actualPath, model.ListArgs{SkipHook: true})
		if e != nil {
			log.Errorf("failed handle objs update hook: %v", e)
		} else {
			op.HandleObjsUpdateHook(ctx, utils.GetFullPath(dstStorage.GetStorage().MountPath, actualPath), files)
			hookedPaths[actualPath] = struct{}{}
		}
	}
	if dstNeedHandleHook {
		handleHook(dstActualPath)
	}
	for _, payload := range payloads {
		switch p := payload.(type) {
		case DstPathToHook:
			if dstNeedHandleHook {
				handleHook(string(p))
			}
		case SrcPathToRemove:
			srcStorage, srcActualPath, err := op.GetStorageAndActualPath(string(p))
			if err != nil {
				log.Error(errors.WithMessage(err, "failed get src storage"))
				continue
			}
			err = verifyAndRemove(ctx, srcStorage, dstStorage, srcActualPath, dstActualPath)
			if err != nil {
				log.Error(err)
			}
		}
	}
}

func verifyAndRemove(ctx context.Context, srcStorage, dstStorage driver.Driver, srcPath, dstPath string) error {
	srcObj, err := op.GetUnwrap(ctx, srcStorage, srcPath)
	if err != nil {
		return errors.WithMessagef(err, "failed get src [%s] file", path.Join(srcStorage.GetStorage().MountPath, srcPath))
	}

	dstObjPath := path.Join(dstPath, srcObj.GetName())
	dstObj, err := op.GetUnwrap(ctx, dstStorage, dstObjPath)
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

	hasErr := false
	for _, obj := range srcObjs {
		srcSubPath := path.Join(srcPath, obj.GetName())
		err := verifyAndRemove(ctx, srcStorage, dstStorage, srcSubPath, dstObjPath)
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

var TransferCoordinator *TaskGroupCoordinator = NewTaskGroupCoordinator("HookAndRemove", HookAndRemove)
