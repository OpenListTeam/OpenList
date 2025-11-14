package fs

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/pkg/errors"
)

func BatchMove(ctx context.Context, srcDirPath, dstDirPath string, objectNames []string) (bool, error) {
	return batchFsOperate(ctx, srcDirPath, dstDirPath, objectNames,
		func(d driver.Driver) bool {
			_, ok := d.(driver.BatchMove)
			return ok
		},
		op.BatchMove,
	)
}

func BatchCopy(ctx context.Context, srcDirPath, dstDirPath string, objectNames []string) (bool, error) {
	return batchFsOperate(ctx, srcDirPath, dstDirPath, objectNames,
		func(d driver.Driver) bool {
			_, ok := d.(driver.BatchCopy)
			return ok
		},
		op.BatchCopy,
	)
}

func BatchRemove(ctx context.Context, srcDirPath string, objectNames []string) (bool, error) {

	srcStorage, srcActualPath, err := op.GetStorageAndActualPath(srcDirPath)
	if err != nil {
		return false, errors.WithMessage(err, "failed to get source storage")
	}

	if _, ok := srcStorage.(driver.BatchRemove); !ok {
		return false, nil
	} else {
		err1 := op.BatchRemove(ctx, srcStorage, srcActualPath, objectNames)
		if err1 != nil {
			return false, err1
		}
		return true, nil
	}

}

func BatchRename(ctx context.Context, srcDirPath string, nameMapping map[string]string) (bool, error) {
	srcStorage, srcActualPath, err := op.GetStorageAndActualPath(srcDirPath)
	if err != nil {
		return false, errors.WithMessage(err, "failed to get source storage")
	}

	if _, ok := srcStorage.(driver.BatchRemove); !ok {
		return false, nil
	} else {
		err1 := op.BatchRename(ctx, srcStorage, srcActualPath, nameMapping)
		if err1 != nil {
			return false, err1
		}
		return true, nil
	}

}

func batchFsOperate(ctx context.Context, srcDirPath, dstDirPath string, objectNames []string,
	capabilityCheck func(d driver.Driver) bool,
	operation func(ctx context.Context, storage driver.Driver, srcPath, dstPath string, names []string) error) (bool, error) {

	srcStorage, srcActualPath, err := op.GetStorageAndActualPath(srcDirPath)
	if err != nil {
		return false, errors.WithMessage(err, "failed to get source storage")
	}
	dstStorage, dstActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		return false, errors.WithMessage(err, "failed to get destination storage")
	}

	if srcStorage.GetStorage() != dstStorage.GetStorage() {
		return false, nil
	}

	if !capabilityCheck(srcStorage) {
		return false, nil
	}

	err = operation(ctx, srcStorage, srcActualPath, dstActualPath, objectNames)
	if err != nil {
		return false, err
	}

	return true, nil
}
