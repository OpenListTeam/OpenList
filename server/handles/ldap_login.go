package handles

import (
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/go-cache"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
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
	// Reject locked clients before the user lookup so a locked IP cannot
	// distinguish "LDAP disabled for this account" (403) from lockout (429).
	ip := c.ClientIP()
	count, ok := model.LoginCache.Get(ip)
	if ok && count >= model.DefaultMaxAuthRetries {
		common.ErrorStrResp(c, model.TooManyAttempts, 429)
		return
	}

	user, err := op.GetUserByName(req.Username)
	if err != nil && !errs.IsRecordNotFound(err) {
		common.ErrorUserLookupResp(c, err)
		return
	}
	if err == nil && !user.AllowLdap {
		common.ErrorStrResp(c, "login via ldap is not allowed", 403)
		return
	}

	err = common.HandleLdapLogin(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, common.ErrFailedLdapAuth) {
			model.LoginCache.Set(ip, count+1, cache.WithEx[int](model.DefaultLockDuration))
			common.ErrorStrResp(c, model.InvalidUsernameOrPassword, 400)
		} else {
			common.ErrorUserLookupResp(c, err)
		}
		return
	}

	if user == nil {
		user, err = common.LdapRegister(req.Username)
		if err != nil {
			if errs.IsDatabaseBusy(err) {
				common.ErrorUserLookupResp(c, err)
				return
			}
			log.Errorf("ldap register failed: %+v", err)
			common.ErrorStrResp(c, "failed to register ldap user", 400)
			model.LoginCache.Set(ip, count+1, cache.WithEx[int](model.DefaultLockDuration))
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
	model.LoginCache.Del(ip)
}
