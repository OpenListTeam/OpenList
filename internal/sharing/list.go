package sharing

import (
	"context"
	stdpath "path"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
)

func list(ctx context.Context, sid, path string, args model.SharingListArgs) (*model.Sharing, []model.Obj, error) {
	sharing, err := op.GetSharingById(sid, args.Refresh)
	if err != nil {
		return nil, nil, errors.WithStack(errs.SharingNotFound)
	}
	if !sharing.Valid() {
		return sharing, nil, errors.WithStack(errs.InvalidSharing)
	}
	if !sharing.Verify(args.Pwd) {
		return sharing, nil, errors.WithStack(errs.WrongShareCode)
	}
	path = utils.FixAndCleanPath(path)
	if len(sharing.Files) == 1 || path != "/" {
		unwrapPath, err := op.GetSharingUnwrapPath(sharing, path)
		if err != nil {
			return nil, nil, errors.WithMessage(err, "failed get sharing unwrap path")
		}
		virtualFiles := op.GetStorageVirtualFilesByPath(unwrapPath)
		storage, actualPath, err := op.GetStorageAndActualPath(unwrapPath)
		if err != nil && len(virtualFiles) == 0 {
			return nil, nil, errors.WithMessage(err, "failed list sharing")
		}
		var objs []model.Obj
		if storage != nil {
			objs, err = op.List(ctx, storage, actualPath, model.ListArgs{
				Refresh: args.Refresh,
				ReqPath: stdpath.Join(sid, path),
			})
			if err != nil && len(virtualFiles) == 0 {
				return nil, nil, errors.WithMessage(err, "failed list sharing")
			}
		}
		om := model.NewObjMerge()
		objs = om.Merge(objs, virtualFiles...)
		model.SortFiles(objs, sharing.OrderBy, sharing.OrderDirection)
		model.ExtractFolder(objs, sharing.ExtractFolder)
		return sharing, objs, nil
	}
	objs := make([]model.Obj, 0, len(sharing.Files))
	for _, f := range sharing.Files {
		if f != "/" {
			isVf := false
			virtualFiles := op.GetStorageVirtualFilesByPath(stdpath.Dir(f))
			for _, vf := range virtualFiles {
				if vf.GetName() == stdpath.Base(f) {
					objs = append(objs, vf)
					isVf = true
					break
				}
			}
			if isVf {
				continue
			}
		} else {
			continue
		}
		storage, actualPath, err := op.GetStorageAndActualPath(f)
		if err != nil {
			continue
		}
		obj, err := op.Get(ctx, storage, actualPath)
		if err != nil {
			continue
		}
		objs = append(objs, obj)
	}
	model.SortFiles(objs, sharing.OrderBy, sharing.OrderDirection)
	model.ExtractFolder(objs, sharing.ExtractFolder)
	return sharing, objs, nil
}
