package fs

import (
	"context"
	"path"

	"github.com/OpenListTeam/OpenList/v4/internal/authz"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
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
	objs := om.Merge(_objs, virtualFiles...)
	objs = authz.FilterHidden(user, meta, path, objs)
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
		if authz.CanRead(user, meta, objPath) {
			result = append(result, obj)
		}
	}
	return result, nil
}
