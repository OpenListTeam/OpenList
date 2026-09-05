package db

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/pkg/errors"
)

// GetCronJobs 返回全部计划任务配置。
// 当前计划任务只允许管理员创建和维护，因此不需要分页和按用户过滤。
func GetCronJobs() ([]model.CronJob, error) {
	var jobs []model.CronJob
	// 固定按 ID 排序，保证前端列表顺序稳定。
	err := db.Order(columnName("id")).Find(&jobs).Error
	return jobs, errors.WithStack(err)
}

// GetCronJobByID 按主键读取单条计划任务；不存在时返回 GORM 的 ErrRecordNotFound。
func GetCronJobByID(id uint) (*model.CronJob, error) {
	var job model.CronJob
	if err := db.First(&job, id).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	return &job, nil
}

// CreateCronJob 创建计划任务；调用方应先完成类型、参数和 cron 表达式校验。
func CreateCronJob(job *model.CronJob) error {
	return errors.WithStack(db.Create(job).Error)
}

// UpdateCronJob 更新计划任务；使用 GORM Save 全量保存由 API 层构造后的记录。
func UpdateCronJob(job *model.CronJob) error {
	return errors.WithStack(db.Save(job).Error)
}

// DeleteCronJobByID 删除计划任务。
// 正在运行的任务由调度器的运行锁保护，不能只依赖这里的数据库状态。
func DeleteCronJobByID(id uint) error {
	return errors.WithStack(db.Delete(&model.CronJob{}, id).Error)
}

// SetCronJobRuntime 只更新运行状态与时间信息，避免并发执行时覆盖管理员刚修改的配置。
func SetCronJobRuntime(id uint, running bool, lastRunAt, nextRunAt *time.Time, lastError string) error {
	updates := map[string]interface{}{
		"running": running,
	}
	// last_run_at / next_run_at 都允许为空，因此不能直接用 struct updates 忽略 nil。
	if lastRunAt != nil {
		updates["last_run_at"] = *lastRunAt
	}
	if nextRunAt != nil {
		updates["next_run_at"] = *nextRunAt
	} else {
		updates["next_run_at"] = nil
	}
	updates["last_error"] = lastError
	return errors.WithStack(db.Model(&model.CronJob{}).Where("id = ?", id).Updates(updates).Error)
}

// ResetRunningCronJobs 在进程启动时把所有残留的 running 标记清空。
// 这些状态只存在于当前进程语义中，重启后不可能仍有线程在执行。
func ResetRunningCronJobs() error {
	return errors.WithStack(db.Model(&model.CronJob{}).Where("running = ?", true).Update("running", false).Error)
}
