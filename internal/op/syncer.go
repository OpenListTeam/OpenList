package op

import (
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func CreateSyncerTaskArgs(syncerTaskArgs *model.SyncerTaskArgs) error {
	return db.CreateSyncerTaskArgs(syncerTaskArgs)
}

func DeleteSyncerTaskArgsById(id uint) error {
	_, err := db.GetSyncerTaskArgsById(id)
	if err != nil {
		return err
	}
	// todo 判断任务是否正在运行
	return db.DeleteSyncerTaskArgsById(id)
}

func UpdateSyncerTaskArgs(syncerTaskArgs *model.SyncerTaskArgs) error {
	_, err := db.GetSyncerTaskArgsById(syncerTaskArgs.ID)
	if err != nil {
		return err
	}
	// todo 判断任务是否正在运行
	return db.UpdateSyncerTaskArgs(syncerTaskArgs)
}

func GetTaskArgsById(id uint) (*model.SyncerTaskArgs, error) {
	return db.GetSyncerTaskArgsById(id)
}

func GetTaskArgs(pageIndex, pageSize int) (users []model.SyncerTaskArgs, count int64, err error) {
	return db.GetSyncerTaskArgs(pageIndex, pageSize)
}
