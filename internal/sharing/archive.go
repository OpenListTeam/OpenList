package sharing

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
)

func archiveMeta(ctx context.Context, sid, path string, args model.SharingArchiveMetaArgs) (*model.Sharing, *model.ArchiveMetaProvider, error) {
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
		storage, actualPath, err := op.GetStorageAndActualPath(unwrapPath)
		if err != nil {
			return nil, nil, errors.WithMessage(err, "failed get sharing file")
		}
		obj, err := op.GetArchiveMeta(ctx, storage, actualPath, args.ArchiveMetaArgs)
		return sharing, obj, err
	}
	return nil, nil, errors.New("cannot get sharing root archive meta")
}

func archiveList(ctx context.Context, sid, path string, args model.SharingArchiveListArgs) (*model.Sharing, []model.Obj, error) {
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
		storage, actualPath, err := op.GetStorageAndActualPath(unwrapPath)
		if err != nil {
			return nil, nil, errors.WithMessage(err, "failed get sharing file")
		}
		obj, err := op.ListArchive(ctx, storage, actualPath, args.ArchiveListArgs)
		return sharing, obj, err
	}
	return nil, nil, errors.New("cannot get sharing root archive list")
}
