package model

import "time"

// CronJob 表示一条持久化的计划任务配置。
// 计划任务本身是通用能力，不同的 Type 会解析不同的 Args；
// 当前内置的任务类型是 sync（rclone 风格的目录同步），后续可以继续注册其他类型。
type CronJob struct {
	// ID 是数据库主键，同时也用于调度器防止同一个任务并发执行。
	ID uint `json:"id" gorm:"primaryKey"`
	// Name 是管理员可读的任务名称，便于在前端区分多条计划任务。
	Name string `json:"name" gorm:"unique" binding:"required"`
	// Type 是计划任务类型，例如 sync；调度器会根据该字段找到对应 Handler。
	Type string `json:"type" binding:"required"`
	// CronSpec 是标准 5 字段 cron 表达式：分 时 日 月 周。
	CronSpec string `json:"cron_spec" binding:"required"`
	// Args 是任务类型的 JSON 配置；这里使用 text 保存，避免每种任务类型都修改表结构。
	Args string `json:"args" gorm:"type:text"`
	// Enabled 为 false 时调度器会跳过执行。
	Enabled bool `json:"enabled"`
	// Running 表示调度器或手动触发的本次执行是否仍在进行。
	// 该字段只用于展示；进程启动时会统一重置，防止崩溃后残留 true。
	Running bool `json:"running"`
	// LastRunAt 是最近一次触发时间（无论成功或失败都会记录）。
	LastRunAt *time.Time `json:"last_run_at"`
	// NextRunAt 是下一次计划触发时间；手动触发不会改变下一次计划时间。
	NextRunAt *time.Time `json:"next_run_at"`
	// LastError 是最近一次执行的错误摘要，为空表示最近一次执行成功。
	LastError string `json:"last_error" gorm:"type:text"`
}
