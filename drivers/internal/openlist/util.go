package openlist

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

type Identity struct {
	Username string
	Guest    bool
}

func Init(d State, getIdentity func(State) (Identity, error)) error {
	*d.Address = strings.TrimSuffix(*d.Address, "/")
	identity, err := getIdentity(d)
	if err != nil {
		return err
	}
	if *d.Username != identity.Username {
		if err := d.Login(); err != nil {
			return err
		}
	}
	identity, err = getIdentity(d)
	if err != nil || !identity.Guest {
		return err
	}
	res, err := base.RestyClient.R().Get(*d.Address + "/api/public/settings")
	if err != nil {
		return err
	}
	if utils.Json.Get(res.Body(), "data", conf.AllowMounted).ToString() != "true" {
		return fmt.Errorf("the site does not allow mounted")
	}
	return nil
}

func (d State) Login() error {
	if *d.Username == "" {
		return nil
	}
	var resp common.Resp[LoginResp]
	_, _, err := d.Request("/auth/login", http.MethodPost, func(req *resty.Request) {
		req.SetResult(&resp).SetBody(base.Json{
			"username": *d.Username,
			"password": *d.Password,
		})
	})
	if err != nil {
		return err
	}
	*d.Token = resp.Data.Token
	op.MustSaveDriverStorage(d.Driver)
	return nil
}

func (d State) Request(api, method string, callback base.ReqCallback, retry ...bool) ([]byte, int, error) {
	url := *d.Address + "/api" + api
	req := base.RestyClient.R()
	req.SetHeader("Authorization", *d.Token)
	if callback != nil {
		callback(req)
	}
	res, err := req.Execute(method, url)
	if err != nil {
		code := 0
		if res != nil {
			code = res.StatusCode()
		}
		return nil, code, err
	}
	log.Debugf("[openlist] response body: %s", res.String())
	if res.StatusCode() >= 400 {
		return nil, res.StatusCode(), fmt.Errorf("request failed, status: %s", res.Status())
	}
	code := utils.Json.Get(res.Body(), "code").ToInt()
	if code != 200 {
		if (code == 401 || code == 403) && !utils.IsBool(retry...) {
			err = d.Login()
			if err != nil {
				return nil, code, err
			}
			return d.Request(api, method, callback, true)
		}
		return nil, code, fmt.Errorf("request failed,code: %d, message: %s", code, utils.Json.Get(res.Body(), "message").ToString())
	}
	return res.Body(), 200, nil
}
