package handles

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/cronjob"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// CronJobReq 是创建/更新计划任务时的请求体。
// Args 保持 json.RawMessage，方便不同任务类型定义不同配置对象。
type CronJobReq struct {
	// Name 是任务名称。
	Name string `json:"name" binding:"required"`
	// Type 是任务类型，例如 sync。
	Type string `json:"type" binding:"required"`
	// CronSpec 是标准 5 字段 cron 表达式。
	CronSpec string `json:"cron_spec" binding:"required"`
	// Enabled 表示是否启用计划任务。
	Enabled bool `json:"enabled"`
	// Args 是任务类型对应的 JSON 配置。
	Args json.RawMessage `json:"args"`
}

// toModel 将 API 请求转换为数据库模型。
func (r *CronJobReq) toModel() (*model.CronJob, error) {
	// Args 必须是合法 JSON；具体字段由任务类型的 Handler 校验。
	args := string(r.Args)
	if args == "" {
		args = "{}"
	}
	return &model.CronJob{
		Name:     r.Name,
		Type:     r.Type,
		CronSpec: r.CronSpec,
		Enabled:  r.Enabled,
		Args:     args,
	}, nil
}

// validateCronJobReq 校验 cron 表达式、任务类型和任务参数。
func validateCronJobReq(req *CronJobReq) error {
	if _, err := cron.ParseCronSpec(req.CronSpec); err != nil {
		return fmt.Errorf("invalid cron spec: %w", err)
	}
	handler, err := cronjob.LookupHandler(req.Type)
	if err != nil {
		return err
	}
	return handler.ValidateArgs(req.Args)
}

// ListCronJobs 返回全部计划任务配置。
func ListCronJobs(c *gin.Context) {
	jobs, err := db.GetCronJobs()
	if err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	// 当前计划任务数量通常很少；先不做分页，保持前端实现简单。
	common.SuccessResp(c, jobs)
}

// ListCronJobTypes 返回当前注册且支持前端配置的任务类型描述。
// 前端根据这里返回的 Fields 动态渲染表单，后续新增任务类型无需修改通用页面结构。
func ListCronJobTypes(c *gin.Context) {
	common.SuccessResp(c, cronjob.RegisteredHandlerInfos())
}

// GetCronJob 按主键返回一条计划任务，供独立编辑页直接读取当前配置。
func GetCronJob(c *gin.Context) {
	id, err := strconv.ParseUint(c.Query("id"), 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "invalid id", 400)
		return
	}
	job, err := db.GetCronJobByID(uint(id))
	if err != nil {
		common.ErrorResp(c, err, 404, true)
		return
	}
	common.SuccessResp(c, job)
}

// CreateCronJob 创建一条计划任务。
func CreateCronJob(c *gin.Context) {
	var req CronJobReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if err := validateCronJobReq(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	job, err := req.toModel()
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	// 创建后从当前时间计算下一次触发时间；不立刻执行任务。
	spec, err := cron.ParseCronSpec(job.CronSpec)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if job.Enabled {
		next := spec.NextAfter(time.Now())
		job.NextRunAt = &next
	}
	if err := db.CreateCronJob(job); err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, job)
}

// UpdateCronJob 更新一条计划任务；正在运行的任务不允许修改，避免配置与执行状态割裂。
func UpdateCronJob(c *gin.Context) {
	var req CronJobReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	id, err := strconv.ParseUint(c.Query("id"), 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "invalid id", 400)
		return
	}
	old, err := db.GetCronJobByID(uint(id))
	if err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	if cronjob.IsRunning(old.ID) {
		common.ErrorStrResp(c, "cronjob is running", 409)
		return
	}
	if err := validateCronJobReq(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	job, err := req.toModel()
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	// 保留数据库主键，并重置下一次执行时间；调度器会在 30 秒内看到新配置。
	job.ID = old.ID
	spec, err := cron.ParseCronSpec(job.CronSpec)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if job.Enabled {
		next := spec.NextAfter(time.Now())
		job.NextRunAt = &next
	} else {
		job.NextRunAt = nil
	}
	if err := db.UpdateCronJob(job); err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, job)
}

// DeleteCronJob 删除一条计划任务；正在运行的任务不允许删除。
func DeleteCronJob(c *gin.Context) {
	id, err := strconv.ParseUint(c.Query("id"), 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "invalid id", 400)
		return
	}
	if cronjob.IsRunning(uint(id)) {
		common.ErrorStrResp(c, "cronjob is running", 409)
		return
	}
	if err := db.DeleteCronJobByID(uint(id)); err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c)
}

// RunCronJob 手动触发一次计划任务；下一个 cron 时间不会因此改变。
func RunCronJob(c *gin.Context) {
	id, err := strconv.ParseUint(c.Query("id"), 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "invalid id", 400)
		return
	}
	if err := cronjob.RunJob(uint(id)); err != nil {
		common.ErrorResp(c, err, 409)
		return
	}
	common.SuccessResp(c)
}
