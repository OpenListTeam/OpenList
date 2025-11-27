package op

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
)

func BatchMove(ctx context.Context, storage driver.Driver, srcDirPath, dstDirPath string, movingObjs []string) error {

	return batchOperate(ctx, storage, srcDirPath, dstDirPath, movingObjs, func(storage driver.Driver, srcDir, dstDir model.Obj, changingObjs []model.Obj) error {
		batchOperator, ok := storage.(driver.BatchMove)
		if !ok {
			return errors.New("storage driver doesn't support batch move")
		}

		err := batchOperator.BatchMove(ctx, srcDir, changingObjs, dstDir, model.BatchArgs{
			SrcDirActualPath: srcDirPath,
			DstDirActualPath: dstDirPath,
		})

		if err != nil {
			return err
		}

		for _, obj := range changingObjs {
			Cache.removeDirectoryObject(storage, srcDirPath, obj)
		}

		return err
	})

}

func BatchCopy(ctx context.Context, storage driver.Driver, srcDirPath, dstDirPath string, copingObjs []string) error {

	return batchOperate(ctx, storage, srcDirPath, dstDirPath, copingObjs, func(storage driver.Driver, srcDir, dstDir model.Obj, changingObjs []model.Obj) error {
		batchOperator, ok := storage.(driver.BatchCopy)
		if !ok {
			return errors.New("storage driver doesn't support batch copy")
		}

		return batchOperator.BatchCopy(ctx, srcDir, changingObjs, dstDir, model.BatchArgs{
			SrcDirActualPath: srcDirPath,
			DstDirActualPath: dstDirPath,
		})
	})

}

func BatchRemove(ctx context.Context, storage driver.Driver, srcDirPath string, removingObjs []string) error {

	batchOperator, ok := storage.(driver.BatchRemove)
	if !ok {
		return errors.New("storage driver doesn't support batch remove")
	}

	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}

	srcDirPath = utils.FixAndCleanPath(srcDirPath)

	srcDirFiles, err := List(ctx, storage, srcDirPath, model.ListArgs{})
	if err != nil {
		return err
	}

	srcDir, err := Get(ctx, storage, srcDirPath)
	if err != nil {
		return err
	}

	removingNameSet := make(map[string]bool)
	for _, obj := range removingObjs {
		removingNameSet[obj] = true
	}

	batchRemoveObj := model.BatchRemoveObj{
		Dir: srcDir,
	}
	for _, obj := range srcDirFiles {
		if removingNameSet[obj.GetName()] {
			batchRemoveObj.RemoveObjs = append(batchRemoveObj.RemoveObjs, obj)
		}
	}

	err = batchOperator.BatchRemove(ctx, batchRemoveObj, model.BatchArgs{
		SrcDirActualPath: srcDirPath,
	})
	if err != nil {
		return err
	}

	for _, obj := range batchRemoveObj.RemoveObjs {
		Cache.removeDirectoryObject(storage, srcDirPath, obj)
	}

	return nil
}

func BatchRename(ctx context.Context, storage driver.Driver, srcDirPath string, nameMapping map[string]string) error {

	batchOperator, ok := storage.(driver.BatchRename)
	if !ok {
		return errors.New("storage driver doesn't support batch rename")
	}

	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}

	srcDirPath = utils.FixAndCleanPath(srcDirPath)

	srcDirFiles, err := List(ctx, storage, srcDirPath, model.ListArgs{})
	if err != nil {
		return err
	}

	srcDir, err := Get(ctx, storage, srcDirPath)
	if err != nil {
		return err
	}

	batchRenameObj := model.BatchRenameObj{
		Dir: srcDir,
	}
	for _, obj := range srcDirFiles {
		if newName, exist := nameMapping[obj.GetName()]; exist {
			batchRenameObj.RenameObjs = append(batchRenameObj.RenameObjs, model.RenameObj{
				Obj:     obj,
				NewName: newName,
			})
		}
	}

	err = batchOperator.BatchRename(ctx, batchRenameObj, model.BatchArgs{
		SrcDirActualPath: srcDirPath,
	})
	if err != nil {
		return err
	}

	Cache.DeleteDirectory(storage, srcDirPath)

	return nil
}

func batchOperate(ctx context.Context, storage driver.Driver, srcDirPath, dstDirPath string, changingObjs []string,
	operation func(storage driver.Driver, srcDir, dstDir model.Obj, changingObjs []model.Obj) error) error {

	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}

	srcDirPath = utils.FixAndCleanPath(srcDirPath)
	dstDirPath = utils.FixAndCleanPath(dstDirPath)

	if srcDirPath == dstDirPath {
		return errors.New("src and dst can't be the same")
	}

	srcDirFiles, err := List(ctx, storage, srcDirPath, model.ListArgs{})
	if err != nil {
		return err
	}

	srcDir, err := Get(ctx, storage, srcDirPath)
	if err != nil {
		return err
	}

	dstDir, err := Get(ctx, storage, dstDirPath)
	if err != nil {
		return err
	}

	changingObjMap := make(map[string]bool, len(changingObjs))
	for _, obj := range changingObjs {
		changingObjMap[obj] = true
	}

	var changingFiles []model.Obj
	for _, file := range srcDirFiles {
		if changingObjMap[file.GetName()] {
			changingFiles = append(changingFiles, file)
		}
	}

	if len(changingFiles) == 0 {
		return errors.New("file doesn't exist")
	}

	err = operation(storage, srcDir, dstDir, changingFiles)
	if err != nil {
		return err
	}

	Cache.DeleteDirectory(storage, dstDirPath)

	return nil

}
