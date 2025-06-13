package thunder

import (
	"context"
	"time"

	"github.com/OpenListTeam/OpenList/drivers/thunder"
	"github.com/OpenListTeam/OpenList/internal/op"
	"github.com/OpenListTeam/OpenList/pkg/singleflight"
	"github.com/Xhofe/go-cache"
)

var (
	taskCache = cache.NewMemCache(cache.WithShards[[]thunder.OfflineTask](16))
	taskG     singleflight.Group[[]thunder.OfflineTask]
)

func (t *Thunder) GetTasks(thunderDriver *thunder.Thunder) ([]thunder.OfflineTask, error) {
	key := op.Key(thunderDriver, "/drive/v1/task")
	if !t.refreshTaskCache {
		if tasks, ok := taskCache.Get(key); ok {
			return tasks, nil
		}
	}
	t.refreshTaskCache = false
	tasks, err, _ := taskG.Do(key, func() ([]thunder.OfflineTask, error) {
		ctx := context.Background()
		tasks, err := thunderDriver.OfflineList(ctx, "")
		if err != nil {
			return nil, err
		}
		// 添加缓存 10s
		if len(tasks) > 0 {
			taskCache.Set(key, tasks, cache.WithEx[[]thunder.OfflineTask](time.Second*10))
		} else {
			taskCache.Del(key)
		}
		return tasks, nil
	})
	if err != nil {
		return nil, err
	}
	return tasks, nil
}
