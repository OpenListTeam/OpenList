package handles

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

func LoginLdap(c *gin.Context) {
	var req LoginReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	enabled := setting.GetBool(conf.LdapLoginEnabled)
	if !enabled {
		common.ErrorStrResp(c, "ldap is not enabled", 403)
		return
	}
	user, err := op.GetUserByName(req.Username)
	if err == nil && !user.AllowLdap {
		common.ErrorStrResp(c, "login via ldap is not allowed", 403)
		return
	}

	// check login lock
	ip := c.ClientIP()
	maxRetries := setting.GetInt(conf.LoginMaxRetries, model.DefaultMaxAuthRetries)
	lockDuration := time.Duration(setting.GetInt(conf.LoginLockDuration, model.DefaultLockDurationMinutes)) * time.Minute

	// check IP blacklist
	blacklist := model.ParseIPList(setting.GetStr(conf.LoginIPBlacklist))
	if model.IsIPBlacklisted(ip, blacklist) {
		common.ErrorStrResp(c, model.TooManyAttempts, 429)
		return
	}

	// check IP whitelist (bypasses lock)
	whitelist := model.ParseIPList(setting.GetStr(conf.LoginIPWhitelist))
	if !model.IsIPWhitelisted(ip, whitelist) && model.CheckLoginLocked(ip, maxRetries, lockDuration) {
		common.ErrorStrResp(c, model.TooManyAttempts, 429)
		return
	}

	err = common.HandleLdapLogin(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, common.ErrFailedLdapAuth) {
			if !model.IsIPWhitelisted(ip, whitelist) {
				model.RecordLoginAttempt(ip)
			}
			common.ErrorResp(c, err, 400)
		} else {
			common.ErrorResp(c, err, 500)
		}
		return
	}

	if user == nil {
		user, err = common.LdapRegister(req.Username)
		if err != nil {
			common.ErrorResp(c, err, 400)
			if !model.IsIPWhitelisted(ip, whitelist) {
				model.RecordLoginAttempt(ip)
			}
			return
		}
	}

	// generate token
	token, err := common.GenerateToken(user)
	if err != nil {
		common.ErrorResp(c, err, 400, true)
		return
	}
	common.SuccessResp(c, gin.H{"token": token})
	model.ClearLoginAttempts(ip)
}
