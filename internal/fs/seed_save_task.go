package fs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/tache"
)

// SeedSaveTask 是「种子秒传 / 中转保存」这类同步操作的进度载体。
//
// 这些操作本身仍然是同步的（HTTP 请求内完成，响应里带逐文件结果），但用户
// 希望在任务列表里看到进度，所以这里额外注册一个任务对象：由调用方在循环里
// 推进度，完成后写入最终状态。
//
// 注意：该 manager 以 Running=false 创建，worker 不会调度任务；Add 之前先把
// 状态置为 Succeeded，是为了让 tache 不把任务放进执行队列（队列没有消费者，
// 会一直堆积）。Add 之后立刻改回 Running，任务就变成「只展示、不执行」。
type SeedSaveTask struct {
	task.TaskExtension
	Title string

	statusMu sync.Mutex
	status   string
}

func (t *SeedSaveTask) GetName() string {
	return t.Title
}

func (t *SeedSaveTask) GetStatus() string {
	t.statusMu.Lock()
	defer t.statusMu.Unlock()
	if t.status == "" {
		return "saving seed"
	}
	return t.status
}

// SetStatus 更新任务列表里显示的阶段文案
func (t *SeedSaveTask) SetStatus(status string) {
	t.statusMu.Lock()
	t.status = status
	t.statusMu.Unlock()
}

// Run 不会被调度执行（见类型注释），保留实现以满足 tache.Task 接口。
func (t *SeedSaveTask) Run() error {
	return nil
}

// Report 记录「第 index/total 个文件」的进度
func (t *SeedSaveTask) Report(index, total int, name, action string) {
	if total <= 0 {
		return
	}
	if index > total {
		index = total
	}
	t.SetStatus(fmt.Sprintf("%s %d/%d: %s", action, index, total, name))
	t.SetProgress(float64(index) / float64(total) * 100)
}

// Finish 结束任务：成功后进度置 100，失败时记录错误
func (t *SeedSaveTask) Finish(err error) {
	t.SetEndTime(time.Now())
	if err != nil {
		t.SetErr(err)
		t.SetState(tache.StateFailed)
		return
	}
	t.SetProgress(100)
	t.SetState(tache.StateSucceeded)
}

var SeedSaveTaskManager *tache.Manager[*SeedSaveTask]

// NewSeedSaveTask 注册一个可见的种子保存任务并返回它。
// 调用方负责在执行过程中调用 Report，结束时调用 Finish。
func NewSeedSaveTask(ctx context.Context, user *model.User, title string) *SeedSaveTask {
	t := &SeedSaveTask{
		TaskExtension: task.TaskExtension{
			Creator: user,
			ApiUrl:  common.GetApiUrl(ctx),
		},
		Title: title,
	}
	now := time.Now()
	t.SetStartTime(now)
	// 先标记成功再 Add：避免 manager 把任务放进无人消费的执行队列（见类型注释）
	t.SetState(tache.StateSucceeded)
	SeedSaveTaskManager.Add(t)
	t.SetState(tache.StateRunning)
	t.SetProgress(0)
	t.SetStatus("正在准备")
	return t
}
