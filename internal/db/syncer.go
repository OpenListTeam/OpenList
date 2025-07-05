package db

import (
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/pkg/errors"
)

func CreateSyncerTaskArgs(syncerTaskArgs *model.SyncerTaskArgs) error {
	return errors.WithStack(db.Create(syncerTaskArgs).Error)
}

func UpdateSyncerTaskArgs(syncerTaskArgs *model.SyncerTaskArgs) error {
	return errors.WithStack(db.Save(syncerTaskArgs).Error)
}

func DeleteSyncerTaskArgsById(id uint) error {
	return errors.WithStack(db.Delete(&model.SyncerTaskArgs{}, id).Error)
}

func GetSyncerTaskArgsById(id uint) (*model.SyncerTaskArgs, error) {
	var syncerTaskArgs model.SyncerTaskArgs
	syncerTaskArgs.ID = id
	if err := db.First(&syncerTaskArgs).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	return &syncerTaskArgs, nil
}

func GetSyncerTaskArgs(pageIndex, pageSize int) (syncTaskArgList []model.SyncerTaskArgs, count int64, err error) {
	syncerTaskArgsDB := db.Model(&model.SyncerTaskArgs{})
	if err := syncerTaskArgsDB.Count(&count).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed get syncTaskArgList count")
	}
	if err := syncerTaskArgsDB.Order(columnName("id")).Offset((pageIndex - 1) * pageSize).Limit(pageSize).Find(&syncTaskArgList).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed get find syncTaskArgList")
	}
	return syncTaskArgList, count, nil
}
