package data

import (
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

var initialTaskItems []model.TaskItem

func initTasks() {
	InitialTasks()

	for i := range initialTaskItems {
		item := &initialTaskItems[i]
		taskitem, _ := db.GetTaskDataByType(item.Key)
		if taskitem == nil {
			db.CreateTaskData(item)
		}
	}
}

func InitialTasks() []model.TaskItem {
	initialTaskItems = []model.TaskItem{
		{Key: "copy", PersistData: "[]"},
		{Key: "move", PersistData: "[]"},
		{Key: "download", PersistData: "[]"},
		{Key: "transfer", PersistData: "[]"},
		// cron_sync 保存同步编排任务；它不是普通的 copy/move 文件传输任务。
		{Key: "cron_sync", PersistData: "[]"},
	}
	return initialTaskItems
}
