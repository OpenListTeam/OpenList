package sharing

import (
	"context"
	stdpath "path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
)

func get(ctx context.Context, sid, path string, args model.SharingListArgs) (*model.Sharing, model.Obj, error) {
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
		if unwrapPath != "/" {
			virtualFiles := op.GetStorageVirtualFilesByPath(stdpath.Dir(unwrapPath))
			for _, f := range virtualFiles {
				if f.GetName() == stdpath.Base(unwrapPath) {
					return sharing, f, nil
				}
			}
		} else {
			return sharing, &model.Object{
				Name:     sid,
				Size:     0,
				Modified: time.Time{},
				IsFolder: true,
			}, nil
		}
		storage, actualPath, err := op.GetStorageAndActualPath(unwrapPath)
		if err != nil {
			return nil, nil, errors.WithMessage(err, "failed get sharing file")
		}
		obj, err := op.Get(ctx, storage, actualPath)
		return sharing, obj, err
	}
	return sharing, &model.Object{
		Name:     sid,
		Size:     0,
		Modified: time.Time{},
		IsFolder: true,
	}, nil
}
