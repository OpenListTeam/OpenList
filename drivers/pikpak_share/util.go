package pikpak_share

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/go-resty/resty/v2"
)

var WebAlgorithms = []string{
	"C9qPpZLN8ucRTaTiUMWYS9cQvWOE",
	"+r6CQVxjzJV6LCV",
	"F",
	"pFJRC",
	"9WXYIDGrwTCz2OiVlgZa90qpECPD6olt",
	"/750aCr4lm/Sly/c",
	"RB+DT/gZCrbV",
	"",
	"CyLsf7hdkIRxRm215hl",
	"7xHvLi2tOYP0Y92b",
	"ZGTXXxu8E/MIWaEDB+Sm/",
	"1UI3",
	"E7fP5Pfijd+7K+t6Tg/NhuLq0eEUVChpJSkrKxpO",
	"ihtqpG6FMt65+Xk+tWUH2",
	"NhXXU9rg4XXdzo7u5o",
}

const (
	WebClientID      = "YUMx5nI8ZU8Ap8pm"
	WebClientSecret  = "dbw2OtmVEeuUvIptb1Coyg"
	WebClientVersion = "2.0.0"
	WebPackageName   = "mypikpak.com"
)

func genDeviceID() string {
	base := []byte("xxxxxxxxxxxx4xxxyxxxxxxxxxxxxxxx")
	random := make([]byte, len(base))
	if _, err := rand.Read(random); err != nil {
		return utils.GetMD5EncodeStr(fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	for i, char := range base {
		switch char {
		case 'x':
			base[i] = "0123456789abcdef"[random[i]&0x0f]
		case 'y':
			base[i] = "0123456789abcdef"[random[i]&0x03|0x08]
		}
	}
	return string(base)
}

func (d *PikPakShare) request(url string, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
	if !d.HasValidCaptchaToken() {
		if err := d.RefreshCaptchaToken(GetAction(method, url), ""); err != nil {
			return nil, err
		}
	}
	req := base.RestyClient.R()
	req.SetHeaders(map[string]string{
		"User-Agent":      d.GetUserAgent(),
		"X-Client-ID":     d.GetClientID(),
		"X-Device-ID":     d.GetDeviceID(),
		"X-Captcha-Token": d.GetCaptchaToken(),
	})

	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}
	var e ErrResp
	req.SetError(&e)
	res, err := req.Execute(method, url)
	if err != nil {
		return nil, err
	}
	switch e.ErrorCode {
	case 0:
		return res.Body(), nil
	case 9: // 验证码token过期
		d.Common.SetCaptchaExpiry(time.Time{})
		d.Common.SetCaptchaToken("")
		if err = d.RefreshCaptchaToken(GetAction(method, url), ""); err != nil {
			return nil, err
		}
		return d.request(url, method, callback, resp)
	case 10: // 操作频繁
		return nil, errors.New(e.ErrorDescription)
	default:
		return nil, errors.New(e.Error())
	}
}

func (d *PikPakShare) getSharePassToken() error {
	query := map[string]string{
		"share_id":       d.ShareId,
		"pass_code":      d.SharePwd,
		"thumbnail_size": "SIZE_LARGE",
		"limit":          "100",
	}
	var resp ShareResp
	_, err := d.request("https://api-drive.mypikpak.net/drive/v1/share", http.MethodGet, func(req *resty.Request) {
		req.SetQueryParams(query)
	}, &resp)
	if err != nil {
		return err
	}
	d.SetPassCodeToken(resp.PassCodeToken)
	return nil
}

func (d *PikPakShare) getFiles(id string) ([]File, error) {
	res := make([]File, 0)
	pageToken := "first"
	for pageToken != "" {
		if pageToken == "first" {
			pageToken = ""
		}
		query := map[string]string{
			"parent_id":       id,
			"share_id":        d.ShareId,
			"thumbnail_size":  "SIZE_LARGE",
			"with_audit":      "true",
			"limit":           "100",
			"filters":         `{"phase":{"eq":"PHASE_TYPE_COMPLETE"},"trashed":{"eq":false}}`,
			"page_token":      pageToken,
			"pass_code_token": d.GetPassCodeToken(),
		}
		var resp ShareResp
		_, err := d.request("https://api-drive.mypikpak.net/drive/v1/share/detail", http.MethodGet, func(req *resty.Request) {
			req.SetQueryParams(query)
		}, &resp)
		if err != nil {
			return nil, err
		}
		if resp.ShareStatus != "OK" {
			if resp.ShareStatus == "PASS_CODE_EMPTY" || resp.ShareStatus == "PASS_CODE_ERROR" {
				err = d.getSharePassToken()
				if err != nil {
					return nil, err
				}
				return d.getFiles(id)
			}
			return nil, errors.New(resp.ShareStatusText)
		}
		pageToken = resp.NextPageToken
		res = append(res, resp.Files...)
	}
	return res, nil
}

func GetAction(method string, url string) string {
	urlpath := regexp.MustCompile(`://[^/]+((/[^/\s?#]+)*)`).FindStringSubmatch(url)[1]
	return method + ":" + urlpath
}

type Common struct {
	client        *resty.Client
	CaptchaToken  string
	CaptchaExpiry time.Time
	// 必要值,签名相关
	ClientID      string
	ClientSecret  string
	ClientVersion string
	PackageName   string
	Algorithms    []string
	DeviceID      string
	UserAgent     string
	stateMu       sync.RWMutex
	refreshMu     sync.Mutex
	// 验证码token刷新成功回调
	RefreshCTokenCk func(token string)
}

func (c *Common) SetUserAgent(userAgent string) {
	c.UserAgent = userAgent
}

func (c *Common) SetCaptchaToken(captchaToken string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.CaptchaToken = captchaToken
}

func (c *Common) SetCaptchaExpiry(expiry time.Time) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.CaptchaExpiry = expiry
}

func (c *Common) SetDeviceID(deviceID string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.DeviceID = deviceID
}

func (c *Common) GetCaptchaToken() string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.CaptchaToken
}

func (c *Common) HasValidCaptchaToken() bool {
	token, expiry, _ := c.captchaSnapshot()
	return hasValidCaptchaToken(token, expiry)
}

func (c *Common) GetClientID() string {
	return c.ClientID
}

func (c *Common) GetUserAgent() string {
	return c.UserAgent
}

func (c *Common) GetDeviceID() string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.DeviceID
}

func (c *Common) captchaSnapshot() (token string, expiry time.Time, deviceID string) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.CaptchaToken, c.CaptchaExpiry, c.DeviceID
}

func (c *Common) setCaptchaState(token string, expiry time.Time) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.CaptchaToken = token
	c.CaptchaExpiry = expiry
}

func hasValidCaptchaToken(token string, expiry time.Time) bool {
	if token == "" {
		return false
	}
	if expiry.IsZero() {
		return true
	}
	return time.Now().Before(expiry.Add(-10 * time.Second))
}

// RefreshCaptchaToken 刷新验证码token
func (d *PikPakShare) RefreshCaptchaToken(action, userID string) error {
	metas := map[string]string{
		"client_version": d.ClientVersion,
		"package_name":   d.PackageName,
		"user_id":        userID,
	}
	metas["timestamp"], metas["captcha_sign"] = d.Common.GetCaptchaSign()
	return d.refreshCaptchaToken(action, metas)
}

// GetCaptchaSign 获取验证码签名
func (c *Common) GetCaptchaSign() (timestamp, sign string) {
	timestamp = fmt.Sprint(time.Now().UnixMilli())
	str := fmt.Sprint(c.ClientID, c.ClientVersion, c.PackageName, c.GetDeviceID(), timestamp)
	for _, algorithm := range c.Algorithms {
		str = utils.GetMD5EncodeStr(str + algorithm)
	}
	sign = "1." + str
	return
}

// refreshCaptchaToken 刷新CaptchaToken
func (d *PikPakShare) refreshCaptchaToken(action string, metas map[string]string) error {
	d.Common.refreshMu.Lock()
	defer d.Common.refreshMu.Unlock()

	oldToken, expiry, deviceID := d.Common.captchaSnapshot()
	if hasValidCaptchaToken(oldToken, expiry) {
		return nil
	}

	param := CaptchaTokenRequest{
		Action:       action,
		CaptchaToken: oldToken,
		ClientID:     d.ClientID,
		DeviceID:     deviceID,
		Meta:         metas,
	}
	var e ErrResp
	var resp CaptchaTokenResponse
	req := base.RestyClient.R().
		SetHeaders(map[string]string{
			"User-Agent":  d.GetUserAgent(),
			"X-Client-ID": d.GetClientID(),
			"X-Device-ID": deviceID,
		}).
		SetError(&e).
		SetResult(&resp).
		SetBody(param)
	_, err := req.Execute(http.MethodPost, "https://user.mypikpak.net/v1/shield/captcha/init")

	if err != nil {
		return err
	}

	if e.IsError() {
		return errors.New(e.Error())
	}

	//if resp.Url != "" {
	//	return fmt.Errorf(`need verify: <a target="_blank" href="%s">Click Here</a>`, resp.Url)
	//}

	d.Common.setCaptchaState(resp.CaptchaToken, resp.Expiry())
	refreshCTokenCk := d.Common.RefreshCTokenCk
	if refreshCTokenCk != nil {
		refreshCTokenCk(resp.CaptchaToken)
	}
	return nil
}
