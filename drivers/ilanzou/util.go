package ilanzou

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/foxxorcat/mopan-sdk-go"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

func (d *ILanZou) login() error {
	res, err := d.unproved("/login", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{"loginName": d.Username, "loginPwd": d.Password})
	})
	if err != nil {
		return err
	}
	d.Token = utils.Json.Get(res, "data", "appToken").ToString()
	if d.Token == "" {
		return fmt.Errorf("failed to login: token is empty, resp: %s", res)
	}
	return nil
}

func getTimestamp(secret []byte) (int64, string, error) {
	ts := time.Now().UnixMilli()
	res, err := mopan.AesEncrypt([]byte(strconv.FormatInt(ts, 10)), secret)
	if err != nil {
		return 0, "", err
	}
	return ts, hex.EncodeToString(res), nil
}

// isCDNChallenge detects a transient CDN challenge (409 Conflict + HTML 403).
// A retry with the session cookie jar can resolve transient challenges.
func isCDNChallenge(res *resty.Response) bool {
	if res == nil || res.StatusCode() != http.StatusConflict {
		return false
	}
	return strings.Contains(res.Header().Get("Content-Type"), "text/html") && strings.Contains(string(res.Body()), "403")
}

func appTokenQueryValue(token string) string {
	// iLanzou requires a literal colon, while other reserved token characters
	// must remain query-escaped.
	return strings.ReplaceAll(url.QueryEscape(token), "%3A", ":")
}

func (d *ILanZou) request(pathname, method string, callback base.ReqCallback, proved bool, retry ...bool) ([]byte, error) {
	_, timestamp, err := getTimestamp(d.conf.secret)
	if err != nil {
		return nil, err
	}
	params := []string{
		"uuid=" + url.QueryEscape(d.UUID),
		"devType=6",
		"devCode=" + url.QueryEscape(d.UUID),
		"devModel=chrome",
		"devVersion=" + url.QueryEscape(d.conf.devVersion),
		"appVersion=",
		"timestamp=" + timestamp,
	}
	if proved {
		params = append(params, "appToken="+appTokenQueryValue(d.Token))
	}
	params = append(params, "extra=2")

	if d.apiClient == nil {
		return nil, fmt.Errorf("iLanzou driver is not initialized")
	}
	req := d.apiClient.R()
	req.SetHeaders(map[string]string{
		"Origin":          d.conf.site,
		"Referer":         d.conf.site + "/",
		"Accept-Encoding": "gzip",
		"Accept-Language": "zh-CN,zh;q=0.9,en-US,en;q=0.8",
	})
	if d.Addition.Ip != "" {
		req.SetHeader("X-Forwarded-For", d.Addition.Ip)
	}
	if callback != nil {
		callback(req)
	}
	res, err := req.Execute(method, d.conf.base+pathname+"?"+strings.Join(params, "&"))
	if err != nil {
		if res != nil {
			log.Errorf("[ilanzou] request error: %s", res.String())
		}
		return nil, err
	}
	isRetry := len(retry) > 0 && retry[0]
	if isCDNChallenge(res) {
		// Resty's cookie jar records the challenge cookie from the first response.
		// A second request proves whether it is a transient challenge or hard block.
		if !isRetry {
			return d.request(pathname, method, callback, proved, true)
		}
		return nil, fmt.Errorf("iLanzou CDN rejected %s with HTML 403; retry later or delete from the console", pathname)
	}
	body := res.Body()
	code := utils.Json.Get(body, "code").ToInt()
	msg := utils.Json.Get(body, "msg").ToString()
	if code != 200 {
		if !isRetry && proved && (utils.SliceContains([]int{-1, -2}, code) || d.Token == "") {
			if err := d.login(); err != nil {
				return nil, err
			}
			return d.request(pathname, method, callback, proved, true)
		}
		return nil, fmt.Errorf("%d: %s", code, msg)
	}
	return body, nil
}

func (d *ILanZou) unproved(pathname, method string, callback base.ReqCallback) ([]byte, error) {
	return d.request("/"+d.conf.unproved+pathname, method, callback, false)
}

func (d *ILanZou) proved(pathname, method string, callback base.ReqCallback) ([]byte, error) {
	return d.request("/"+d.conf.proved+pathname, method, callback, true)
}
