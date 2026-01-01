package fs

import (
	"context"
	stdpath "path"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
)

func get(ctx context.Context, path string, args *GetArgs) (model.Obj, error) {
	path = utils.FixAndCleanPath(path)
	// maybe a virtual file
	if path != "/" {
		dir, name := stdpath.Split(path)
		virtualFiles := op.GetStorageVirtualFilesWithDetailsByPath(ctx, dir, !args.WithStorageDetails, false, name)
		for _, f := range virtualFiles {
			if f.GetName() == name {
				return f, nil
			}
		}
	}
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		// if there are no storage prefix with path, maybe root folder
		if path == "/" {
			return &model.Object{
				Name:     "root",
				IsFolder: true,
				Mask:     model.ReadOnly | model.Virtual,
			}, nil
		}
		return nil, errors.WithMessage(err, "failed get storage")
	}
	return op.Get(ctx, storage, actualPath)
}
