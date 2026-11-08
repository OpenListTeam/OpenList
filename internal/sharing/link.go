package sharing

import (
	"context"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
)

func link(ctx context.Context, sid, path string, args *LinkArgs) (*model.Sharing, *model.Link, model.Obj, error) {
	sharing, err := op.GetSharingById(sid, args.SharingListArgs.Refresh)
	if err != nil {
		return nil, nil, nil, errors.WithStack(errs.SharingNotFound)
	}
	path = utils.FixAndCleanPath(path)
	if len(sharing.Files) != 1 && path == "/" {
		return nil, nil, nil, errors.New("cannot get sharing root link")
	}
	if err = op.AccessSharing(args.ClientIP, args.Pwd, sharing); err != nil {
		return sharing, nil, nil, err
	}
	unwrapPath, err := op.GetSharingUnwrapPath(sharing, path)
	if err != nil {
		return nil, nil, nil, errors.WithMessage(err, "failed get sharing unwrap path")
	}
	storage, actualPath, err := op.GetStorageAndActualPath(unwrapPath)
	if err != nil {
		return nil, nil, nil, errors.WithMessage(err, "failed get sharing link")
	}
	l, obj, err := op.Link(ctx, storage, actualPath, args.LinkArgs)
	if err != nil {
		return nil, nil, nil, errors.WithMessage(err, "failed get sharing link")
	}
	if l.URL != "" && !strings.HasPrefix(l.URL, "http://") && !strings.HasPrefix(l.URL, "https://") {
		l.URL = conf.GetApiUrl(ctx) + l.URL
	}
	return sharing, l, obj, nil
}
