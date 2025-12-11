package task

import (
	"context"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/tache"
)

type TaskExtension struct {
	tache.Base
	Creator    *model.User
	startTime  *time.Time
	endTime    *time.Time
	TotalBytes int64
	ApiUrl     string
}

func (t *TaskExtension) SetCtx(ctx context.Context) {
	if t.Creator != nil {
		ctx = context.WithValue(ctx, conf.UserKey, t.Creator)
	}
	if len(t.ApiUrl) > 0 {
		ctx = context.WithValue(ctx, conf.ApiUrlKey, t.ApiUrl)
	}
	t.Base.SetCtx(ctx)
}

func (t *TaskExtension) SetCreator(creator *model.User) {
	t.Creator = creator
	t.Persist()
}

func (t *TaskExtension) GetCreator() *model.User {
	return t.Creator
}

func (t *TaskExtension) SetStartTime(startTime time.Time) {
	t.startTime = &startTime
}

func (t *TaskExtension) GetStartTime() *time.Time {
	return t.startTime
}

func (t *TaskExtension) SetEndTime(endTime time.Time) {
	t.endTime = &endTime
}

func (t *TaskExtension) GetEndTime() *time.Time {
	return t.endTime
}

func (t *TaskExtension) ClearEndTime() {
	t.endTime = nil
}

func (t *TaskExtension) SetTotalBytes(totalBytes int64) {
	t.TotalBytes = totalBytes
}

func (t *TaskExtension) GetTotalBytes() int64 {
	return t.TotalBytes
}

func (t *TaskExtension) SetRetry(retry int, maxRetry int) {
	t.Base.SetRetry(retry, maxRetry)
	if retry > 0 || !conf.Conf.Tasks.AllowRetryCanceled || t.Ctx() == nil {
		return
	}
	select {
	case <-t.Ctx().Done():
		ctx, cancel := context.WithCancel(context.Background())
		t.SetCtx(ctx)
		t.SetCancelFunc(cancel)
	default:
	}
}

type TaskExtensionInfo interface {
	tache.TaskWithInfo
	GetCreator() *model.User
	GetStartTime() *time.Time
	GetEndTime() *time.Time
	GetTotalBytes() int64
}
