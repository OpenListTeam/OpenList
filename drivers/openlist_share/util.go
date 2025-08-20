package openlist_share

import (
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func (d *OpenListShare) request(api, method string, callback base.ReqCallback) ([]byte, int, error) {
	url := d.Address + "/api" + api
	req := base.RestyClient.R()
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
	if res.StatusCode() >= 400 {
		return nil, res.StatusCode(), fmt.Errorf("request failed, status: %s", res.Status())
	}
	code := utils.Json.Get(res.Body(), "code").ToInt()
	if code != 200 {
		return nil, code, fmt.Errorf("request failed, code: %d, message: %s", code, utils.Json.Get(res.Body(), "message").ToString())
	}
	return res.Body(), 200, nil
}
