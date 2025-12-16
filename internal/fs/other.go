package fs

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
)

func makeDir(ctx context.Context, path string) error {
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}
	return op.MakeDir(ctx, storage, actualPath)
}

func rename(ctx context.Context, srcPath, dstName string, skipHook ...bool) error {
	storage, srcActualPath, err := op.GetStorageAndActualPath(srcPath)
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}
	if utils.IsBool(skipHook...) {
		ctx = context.WithValue(ctx, conf.SkipHookKey, struct{}{})
	}
	return op.Rename(ctx, storage, srcActualPath, dstName)
}

func remove(ctx context.Context, path string) error {
	storage, actualPath, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}
	return op.Remove(ctx, storage, actualPath)
}

func other(ctx context.Context, args model.FsOtherArgs) (interface{}, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(args.Path)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get storage")
	}
	args.Path = actualPath
	return op.Other(ctx, storage, args)
}

type TaskData struct {
	task.TaskExtension
	Status        string        `json:"-"` //don't save status to save space
	SrcActualPath string        `json:"src_path"`
	DstActualPath string        `json:"dst_path"`
	SrcStorage    driver.Driver `json:"-"`
	DstStorage    driver.Driver `json:"-"`
	SrcStorageMp  string        `json:"src_storage_mp"`
	DstStorageMp  string        `json:"dst_storage_mp"`
}

func (t *TaskData) GetStatus() string {
	return t.Status
}
