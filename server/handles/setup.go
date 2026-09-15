package handles

import (
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// InitStatus 返回系统是否已完成初始化（即是否已存在管理员账号）。
// 前端据此判断是否跳转到安装向导。
func InitStatus(c *gin.Context) {
	if _, err := op.GetAdmin(); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			common.SuccessResp(c, gin.H{"initialized": false})
			return
		}
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, gin.H{"initialized": true})
}

// InitSetupReq 系统初始化请求体
type InitSetupReq struct {
	Username  string `json:"username" binding:"required"`
	Password  string `json:"password" binding:"required"`
	SiteTitle string `json:"site_title"`
}

// InitSetup 执行系统初始化：创建管理员账号并设置站点名称等初始参数。
// 仅在系统尚未初始化时允许调用；已初始化则返回错误。
func InitSetup(c *gin.Context) {
	var req InitSetupReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	// 已初始化则拒绝
	if _, err := op.GetAdmin(); err == nil {
		common.ErrorStrResp(c, "system has already been initialized", 400)
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	// 密码最小长度校验
	if len(req.Password) < 4 {
		common.ErrorStrResp(c, "password must be at least 4 characters", 400)
		return
	}
	admin := &model.User{
		Username:   req.Username,
		Role:       model.ADMIN,
		BasePath:   "/",
		Authn:      "[]",
		Permission: 0x71FF,
	}
	admin.SetPassword(req.Password)
	if err := op.CreateUser(admin); err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	// 设置站点名称（如果提供）
	if req.SiteTitle != "" {
		item := model.SettingItem{
			Key:   conf.SiteTitle,
			Value: req.SiteTitle,
			Type:  conf.TypeString,
			Group: model.SITE,
			Flag:  model.PUBLIC,
		}
		if err := op.SaveSettingItem(&item); err != nil {
			common.ErrorResp(c, err, 500, true)
			return
		}
	}
	common.SuccessResp(c)
}
