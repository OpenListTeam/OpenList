package cmd

import (
	"crypto/tls"
	"fmt"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	iplugin "github.com/OpenListTeam/OpenList/v4/internal/plugin"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

func DelAdminCacheOnline() {
	admin, err := op.GetAdmin()
	if err != nil {
		utils.Log.Errorf("[del_admin_cache] get admin error: %+v", err)
		return
	}
	DelUserCacheOnline(admin.Username)
}

func DelUserCacheOnline(username string) {
	client := resty.New().SetTimeout(1 * time.Second).SetTLSClientConfig(&tls.Config{InsecureSkipVerify: conf.Conf.TlsInsecureSkipVerify})
	token := setting.GetStr(conf.Token)
	serverPlugin := conf.Conf.Plugin("server")
	if serverPlugin == nil {
		utils.Log.Warnf("[del_user_cache] server plugin config missing")
		return
	}
	port, err := iplugin.IntValue(serverPlugin.Data, "http_port", -1)
	if err != nil {
		utils.Log.Warnf("[del_user_cache] parse server http_port failed: %+v", err)
		return
	}
	u := fmt.Sprintf("http://localhost:%d/api/admin/user/del_cache", port)
	if port == -1 {
		httpsPort, err := iplugin.IntValue(serverPlugin.Data, "https_port", -1)
		if err != nil {
			utils.Log.Warnf("[del_user_cache] parse server https_port failed: %+v", err)
			return
		}
		if httpsPort == -1 {
			utils.Log.Warnf("[del_user_cache] no open port")
			return
		}
		u = fmt.Sprintf("https://localhost:%d/api/admin/user/del_cache", httpsPort)
	}
	res, err := client.R().SetHeader("Authorization", token).SetQueryParam("username", username).Post(u)
	if err != nil {
		utils.Log.Warnf("[del_user_cache_online] failed: %+v", err)
		return
	}
	if res.StatusCode() != 200 {
		utils.Log.Warnf("[del_user_cache_online] failed: %+v", res.String())
		return
	}
	code := utils.Json.Get(res.Body(), "code").ToInt()
	msg := utils.Json.Get(res.Body(), "message").ToString()
	if code != 200 {
		utils.Log.Errorf("[del_user_cache_online] error: %s", msg)
		return
	}
	utils.Log.Debugf("[del_user_cache_online] del user [%s] cache success", username)
}
