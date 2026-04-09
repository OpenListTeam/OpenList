package fs

import (
	"context"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"path"
)

// List files
func list(ctx context.Context, path string, args *ListArgs) ([]model.Obj, error) {
	meta, _ := ctx.Value(conf.MetaKey).(*model.Meta)
	user, _ := ctx.Value(conf.UserKey).(*model.User)
	virtualFiles := op.GetStorageVirtualFilesWithDetailsByPath(ctx, path, !args.WithStorageDetails, args.Refresh, "")
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil && len(virtualFiles) == 0 {
		return nil, errors.WithMessage(err, "failed get storage")
	}

	var _objs []model.Obj
	if storage != nil {
		_objs, err = op.List(ctx, storage, actualPath, model.ListArgs{
			ReqPath:            path,
			Refresh:            args.Refresh,
			WithStorageDetails: args.WithStorageDetails,
		})
		if err != nil {
			if !args.NoLog {
				log.Errorf("fs/list: %+v", err)
			}
			if len(virtualFiles) == 0 {
				return nil, errors.WithMessage(err, "failed get objs")
			}
		}
	}

	om := model.NewObjMerge()
	if whetherHide(user, meta, path) {
		om.InitHideReg(meta.Hide)
	}
	objs := om.Merge(_objs, virtualFiles...)
	objs, err = filterReadableObjs(objs, user, path, meta)
	return objs, err
}

func filterReadableObjs(objs []model.Obj, user *model.User, reqPath string, parentMeta *model.Meta) ([]model.Obj, error) {
	var result []model.Obj
	for _, obj := range objs {
		var meta *model.Meta
		objPath := path.Join(reqPath, obj.GetName())
		if obj.IsDir() {
			var err error
			meta, err = op.GetNearestMeta(objPath)
			if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
				return result, err
			}
		} else {
			meta = parentMeta
		}
		if common.CanRead(user, meta, objPath) {
			result = append(result, obj)
		}
	}
	return result, nil
}

func whetherHide(user *model.User, meta *model.Meta, path string) bool {
	// if is admin, don't hide
	if user == nil || user.CanSeeHides() {
		return false
	}
	// if meta is nil, don't hide
	if meta == nil {
		return false
	}
	// if meta.Hide is empty, don't hide
	if meta.Hide == "" {
		return false
	}
	// if meta doesn't apply to sub_folder, don't hide
	if !common.MetaCoversPath(meta.Path, path, meta.HSub) {
		return false
	}
	// if is guest, hide
	return true
}
