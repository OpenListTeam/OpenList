package cronjob

import (
	"encoding/json"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// getTaskDataFunc 返回 tache 的任务恢复函数。
// 这里先从数据库读取 JSON 文本，再转成 tache 需要的 []byte。
func getTaskDataFunc(key string) func() ([]byte, error) {
	return func() ([]byte, error) {
		item, err := db.GetTaskDataByType(key)
		if err != nil {
			return nil, err
		}
		return []byte(item.PersistData), nil
	}
}

// updateTaskDataFunc 返回 tache 的任务持久化函数。
// tache 传入的是 JSON 字节流；这里只保存文本，不同任务类型通过 key 隔离。
func updateTaskDataFunc(key string) func([]byte) error {
	return func(data []byte) error {
		// 只用于检查输入确实是合法 JSON，避免把损坏的任务状态写入数据库。
		var raw json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		return db.UpdateTaskData(&model.TaskItem{Key: key, PersistData: string(data)})
	}
}
