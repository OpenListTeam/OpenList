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

type requestRetryAction uint8

const (
	requestRetryNone requestRetryAction = iota
	requestRetryCaptcha
	maxSharePassRefreshesPerProgress = 8
)

func classifyRequestError(errResp *ErrResp) (requestRetryAction, error) {
	switch errResp.ErrorCode {
	case 0:
		return requestRetryNone, nil
	case 9:
		return requestRetryCaptcha, nil
	case 10:
		return requestRetryNone, errors.New(errResp.ErrorDescription)
	default:
		return requestRetryNone, errors.New(errResp.Error())
	}
}

func isPassCodeErrorStatus(status string) bool {
	return status == "PASS_CODE_EMPTY" || status == "PASS_CODE_ERROR"
}

func (d *PikPakShare) request(url string, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
	reqAction := GetAction(method, url)
	for attempts := 0; attempts < 3; attempts++ {
		captchaToken, err := d.ensureCaptchaToken(reqAction, "")
		if err != nil {
			return nil, err
		}
		req := base.RestyClient.R()
		req.SetHeaders(map[string]string{
			"User-Agent":      d.GetUserAgent(),
			"X-Client-ID":     d.GetClientID(),
			"X-Device-ID":     d.GetDeviceID(),
			"X-Captcha-Token": captchaToken,
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

		retryAction, err := classifyRequestError(&e)
		if err != nil {
			return nil, err
		}
		if retryAction == requestRetryNone {
			return res.Body(), nil
		}
		d.Common.invalidateCaptchaTokenIfMatch(captchaToken, reqAction)
	}
	return nil, errors.New("request retry limit exceeded")
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
	pagesFetched := 0
	passRefreshesByProgress := make(map[int]int)
	for pageToken != "" {
		if pageToken == "first" {
			pageToken = ""
		}
		currentPassCodeToken := d.GetPassCodeToken()
		query := map[string]string{
			"parent_id":       id,
			"share_id":        d.ShareId,
			"thumbnail_size":  "SIZE_LARGE",
			"with_audit":      "true",
			"limit":           "100",
			"filters":         `{"phase":{"eq":"PHASE_TYPE_COMPLETE"},"trashed":{"eq":false}}`,
			"page_token":      pageToken,
			"pass_code_token": currentPassCodeToken,
		}
		var resp ShareResp
		_, err := d.request("https://api-drive.mypikpak.net/drive/v1/share/detail", http.MethodGet, func(req *resty.Request) {
			req.SetQueryParams(query)
		}, &resp)
		if err != nil {
			return nil, err
		}
		if resp.ShareStatus != "OK" {
			if !isPassCodeErrorStatus(resp.ShareStatus) {
				return nil, errors.New(resp.ShareStatusText)
			}
			latestPassCodeToken := d.GetPassCodeToken()
			if latestPassCodeToken != "" && latestPassCodeToken != currentPassCodeToken {
				res = make([]File, 0)
				pageToken = "first"
				pagesFetched = 0
				continue
			}
			passRefreshesByProgress[pagesFetched]++
			if passRefreshesByProgress[pagesFetched] > maxSharePassRefreshesPerProgress {
				return nil, fmt.Errorf("share pass code token retry limit exceeded after %d fetched pages", pagesFetched)
			}

			if err = d.getSharePassToken(); err != nil {
				return nil, err
			}
			newPassCodeToken := d.GetPassCodeToken()
			if newPassCodeToken == "" {
				return nil, errors.New(resp.ShareStatusText)
			}
			res = make([]File, 0)
			pageToken = "first"
			pagesFetched = 0
			continue
		}
		pageToken = resp.NextPageToken
		res = append(res, resp.Files...)
		pagesFetched++
	}
	return res, nil
}

func GetAction(method string, url string) string {
	urlpath := regexp.MustCompile(`://[^/]+((/[^/\s?#]+)*)`).FindStringSubmatch(url)[1]
	return method + ":" + urlpath
}

type captchaState struct {
	Token  string
	Expiry time.Time
}

type Common struct {
	client        *resty.Client
	captchaStates map[string]captchaState
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

func (c *Common) SetDeviceID(deviceID string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.DeviceID = deviceID
}

func (c *Common) captchaTokenForAction(action string) (string, bool) {
	token, expiry, _ := c.captchaSnapshot(action)
	if !hasValidCaptchaToken(token, expiry) {
		return "", false
	}
	return token, true
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

func (c *Common) captchaSnapshot(action string) (token string, expiry time.Time, deviceID string) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	state := c.captchaStates[action]
	return state.Token, state.Expiry, c.DeviceID
}

func (c *Common) setCaptchaState(action, token string, expiry time.Time) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if token == "" {
		delete(c.captchaStates, action)
		return
	}
	if c.captchaStates == nil {
		c.captchaStates = make(map[string]captchaState)
	}
	c.captchaStates[action] = captchaState{Token: token, Expiry: expiry}
}

func (c *Common) invalidateCaptchaToken() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	clear(c.captchaStates)
}

func (c *Common) invalidateCaptchaTokenIfMatch(expectedToken, expectedAction string) bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	state, ok := c.captchaStates[expectedAction]
	if !ok || state.Token != expectedToken {
		return false
	}
	delete(c.captchaStates, expectedAction)
	return true
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

func (d *PikPakShare) captchaMetas(userID string) map[string]string {
	metas := map[string]string{
		"client_version": d.ClientVersion,
		"package_name":   d.PackageName,
		"user_id":        userID,
	}
	metas["timestamp"], metas["captcha_sign"] = d.Common.GetCaptchaSign()
	return metas
}

// RefreshCaptchaToken 刷新验证码token
func (d *PikPakShare) RefreshCaptchaToken(action, userID string) error {
	_, err := d.ensureCaptchaToken(action, userID)
	return err
}

func (d *PikPakShare) ensureCaptchaToken(action, userID string) (string, error) {
	return d.refreshCaptchaToken(action, d.captchaMetas(userID))
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

func (d *PikPakShare) initCaptchaToken(action string, metas map[string]string, oldToken, deviceID string) (ErrResp, CaptchaTokenResponse, error) {
	e := ErrResp{}
	resp := CaptchaTokenResponse{}
	param := CaptchaTokenRequest{
		Action:       action,
		CaptchaToken: oldToken,
		ClientID:     d.ClientID,
		DeviceID:     deviceID,
		Meta:         metas,
	}
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
	return e, resp, err
}

// refreshCaptchaToken 刷新CaptchaToken
func (d *PikPakShare) refreshCaptchaToken(action string, metas map[string]string) (string, error) {
	d.Common.refreshMu.Lock()
	defer d.Common.refreshMu.Unlock()

	oldToken, expiry, deviceID := d.Common.captchaSnapshot(action)
	if hasValidCaptchaToken(oldToken, expiry) {
		return oldToken, nil
	}
	e, resp, err := d.initCaptchaToken(action, metas, oldToken, deviceID)
	if err != nil {
		return "", err
	}

	if e.IsError() {
		return "", &e
	}

	//if resp.Url != "" {
	//	return fmt.Errorf(`need verify: <a target="_blank" href="%s">Click Here</a>`, resp.Url)
	//}

	d.Common.setCaptchaState(action, resp.CaptchaToken, resp.Expiry())
	refreshCTokenCk := d.Common.RefreshCTokenCk
	if refreshCTokenCk != nil {
		refreshCTokenCk(resp.CaptchaToken)
	}
	return resp.CaptchaToken, nil
}
