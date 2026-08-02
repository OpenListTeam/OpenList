package guangya

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/go-resty/resty/v2"
	"golang.org/x/time/rate"
)

const (
	accountBaseURL = "https://account.guangyapan.com"
	apiBaseURL     = "https://api.guangyapan.com"
	defaultClient  = "aMe-8VSlkrbQXpUR"
)

// apiRateInterval is the minimum gap between two requests to the same endpoint.
const apiRateInterval = 500 * time.Millisecond

type Guangya struct {
	model.Storage
	Addition

	accountClient *resty.Client
	apiClient     *resty.Client

	resolvedRootFolderID string
	rootFolderResolved   bool

	apiRateLimit sync.Map
}

func (d *Guangya) Config() driver.Config {
	return config
}

func (d *Guangya) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Guangya) Drop(ctx context.Context) error {
	return nil
}

// ========== Init ==========

func (d *Guangya) Init(ctx context.Context) error {
	d.ClientID = strings.TrimSpace(d.ClientID)
	if d.ClientID == "" {
		d.ClientID = defaultClient
	}
	d.DeviceID = normalizeDeviceID(d.DeviceID)
	if d.DeviceID == "" {
		d.DeviceID = randomDeviceID()
	}
	if d.PageSize <= 0 {
		d.PageSize = 100
	}
	if d.OrderBy < 0 {
		d.OrderBy = 3
	}
	if d.SortType != 0 && d.SortType != 1 {
		d.SortType = 1
	}

	d.RootPath = strings.TrimSpace(d.RootPath)
	d.AccessToken = strings.TrimSpace(d.AccessToken)
	d.RefreshToken = strings.TrimSpace(d.RefreshToken)
	d.PhoneNumber = strings.TrimSpace(d.PhoneNumber)
	d.VerifyCode = strings.TrimSpace(d.VerifyCode)
	d.CaptchaToken = strings.TrimSpace(d.CaptchaToken)
	d.VerificationID = strings.TrimSpace(d.VerificationID)
	d.resolvedRootFolderID = ""
	d.rootFolderResolved = false

	d.accountClient = base.NewRestyClient().
		SetBaseURL(accountBaseURL).
		SetHeader("Accept", "application/json, text/plain, */*").
		SetHeader("Content-Type", "application/json").
		SetHeader("X-Device-Model", "chrome%2F147.0.0.0").
		SetHeader("X-Device-Name", "PC-Chrome").
		SetHeader("X-Device-Sign", "wdi10."+d.DeviceID+"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx").
		SetHeader("X-Net-Work-Type", "NONE").
		SetHeader("X-OS-Version", "MacIntel").
		SetHeader("X-Platform-Version", "1").
		SetHeader("X-Protocol-Version", "301").
		SetHeader("X-Provider-Name", "NONE").
		SetHeader("X-SDK-Version", "9.0.2").
		SetHeader("X-Client-Id", d.ClientID).
		SetHeader("X-Client-Version", "0.0.1").
		SetHeader("X-Device-Id", d.DeviceID)
	if d.CaptchaToken != "" {
		d.accountClient.SetHeader("X-Captcha-Token", d.CaptchaToken)
	}

	d.apiClient = base.NewRestyClient().
		SetBaseURL(apiBaseURL).
		SetHeader("Accept", "application/json, text/plain, */*").
		SetHeader("Content-Type", "application/json").
		SetHeader("Did", d.DeviceID).
		SetHeader("Dt", "4")

	// Priority: access_token -> refresh_token -> sms login.
	if d.AccessToken != "" {
		if err := d.validateToken(ctx); err == nil {
			return d.prepareRootFolder(ctx)
		}
		d.AccessToken = ""
	}
	if d.RefreshToken != "" {
		if err := d.refreshToken(ctx); err == nil {
			if err2 := d.validateToken(ctx); err2 == nil {
				return d.prepareRootFolder(ctx)
			}
		}
	}
	// Two-stage SMS flow:
	if d.PhoneNumber != "" {
		if d.canSMSLogin() {
			if err := d.loginBySMSCode(ctx); err != nil {
				return err
			}
			if err := d.validateToken(ctx); err != nil {
				return err
			}
			return d.prepareRootFolder(ctx)
		}
		if d.SendCode {
			d.setTempStatus("SMS 发送中...")
			if err := d.prepareSMSCode(ctx); err != nil {
				d.setTempStatus(fmt.Sprintf("SMS 发送失败: %v. 请检查 captcha_token 并重试。", err))
			} else {
				d.setTempStatus("SMS 发送成功。请填写 verify_code 并保存以完成登录。")
			}
		}
		return nil
	}
	return errors.New("登录失败：请提供有效的 access_token、refresh_token 或 phone_number + verify_code")
}

// ========== Auth: validate / refresh ==========

func (d *Guangya) validateToken(ctx context.Context) error {
	var resp commonResp
	_, err := d.apiClient.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetResult(&resp).
		Get("/userres/v1/file/get_file_list?parentId=&page=0&pageSize=1&orderBy=3&sortType=1")
	if err != nil {
		return err
	}
	return nil
}

func (d *Guangya) refreshToken(ctx context.Context) error {
	if d.RefreshToken == "" {
		return errors.New("refresh token is empty")
	}
	var out tokenResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"refresh_token": d.RefreshToken,
			"client_id":     d.ClientID,
		}).
		SetResult(&out).
		Post("/v1/auth/token/refresh")
	if err != nil {
		return err
	}
	if resp.IsError() || out.Error != "" || strings.TrimSpace(out.AccessToken) == "" {
		return fmt.Errorf("refresh token failed: %s", d.accountErr(out.ErrorDesc, out.Error, resp))
	}
	d.AccessToken = strings.TrimSpace(out.AccessToken)
	if strings.TrimSpace(out.RefreshToken) != "" {
		d.RefreshToken = strings.TrimSpace(out.RefreshToken)
	}
	op.MustSaveDriverStorage(d)
	return nil
}

// ========== SMS Login ==========

func (d *Guangya) canSMSLogin() bool {
	return d.PhoneNumber != "" && d.VerifyCode != ""
}

func (d *Guangya) loginBySMSCode(ctx context.Context) error {
	verificationID := strings.TrimSpace(d.VerificationID)
	if verificationID == "" {
		var err error
		verificationID, err = d.requestVerificationID(ctx)
		if err != nil {
			return err
		}
	}

	var step2 verifyResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"verification_id":   verificationID,
			"verification_code": d.VerifyCode,
			"client_id":         d.ClientID,
		}).
		SetResult(&step2).
		Post("/v1/auth/verification/verify")
	if err != nil {
		return err
	}
	if resp.IsError() || step2.Error != "" || strings.TrimSpace(step2.VerificationToken) == "" {
		return fmt.Errorf("验证码验证失败: %s", d.accountErr(step2.ErrorDesc, step2.Error, resp))
	}

	var out tokenResp
	resp, err = d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"verification_code":  d.VerifyCode,
			"verification_token": step2.VerificationToken,
			"username":           normalizePhoneE164(d.PhoneNumber),
			"client_id":          d.ClientID,
		}).
		SetResult(&out).
		Post("/v1/auth/signin")
	if err != nil {
		return err
	}
	if resp.IsError() || out.Error != "" || strings.TrimSpace(out.AccessToken) == "" {
		return fmt.Errorf("登录失败: %s", d.accountErr(out.ErrorDesc, out.Error, resp))
	}

	d.AccessToken = strings.TrimSpace(out.AccessToken)
	d.RefreshToken = strings.TrimSpace(out.RefreshToken)
	d.VerificationID = ""
	d.VerifyCode = ""
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *Guangya) prepareSMSCode(ctx context.Context) error {
	d.VerificationID = ""
	if err := d.ensureCaptchaToken(ctx, false); err != nil {
		return err
	}
	verificationID, err := d.requestVerificationID(ctx)
	if err != nil {
		return err
	}
	d.VerificationID = verificationID
	d.SendCode = false
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *Guangya) requestVerificationID(ctx context.Context) (string, error) {
	if d.CaptchaToken != "" {
		d.accountClient.SetHeader("X-Captcha-Token", d.CaptchaToken)
	}

	var step1 verificationResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"phone_number": normalizePhoneE164(d.PhoneNumber),
			"target":       "ANY",
			"client_id":    d.ClientID,
		}).
		SetResult(&step1).
		Post("/v1/auth/verification")
	if err != nil {
		return "", err
	}
	if resp.IsError() || step1.Error != "" || strings.TrimSpace(step1.VerificationID) == "" {
		if strings.Contains(step1.Error, "captcha_invalid") || strings.Contains(step1.ErrorDesc, "captcha_token expired") {
			if err := d.ensureCaptchaToken(ctx, true); err == nil {
				return d.requestVerificationID(ctx)
			}
		}
		return "", fmt.Errorf("请求验证码失败: %s", d.accountErr(step1.ErrorDesc, step1.Error, resp))
	}
	return strings.TrimSpace(step1.VerificationID), nil
}

func (d *Guangya) ensureCaptchaToken(ctx context.Context, force bool) error {
	if !force && d.CaptchaToken != "" {
		d.accountClient.SetHeader("X-Captcha-Token", d.CaptchaToken)
		return nil
	}

	var out captchaInitResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"client_id": d.ClientID,
			"action":    "POST:/v1/auth/verification",
			"device_id": d.DeviceID,
			"meta": map[string]any{
				"username":           normalizePhoneE164(d.PhoneNumber),
				"phone_number":       normalizePhoneE164(d.PhoneNumber),
				"VERIFICATION_PHONE": normalizePhoneE164(d.PhoneNumber),
			},
		}).
		SetResult(&out).
		Post("/v1/shield/captcha/init")
	if err != nil {
		return err
	}
	if resp.IsError() || out.Error != "" || strings.TrimSpace(out.CaptchaToken) == "" {
		return fmt.Errorf("获取 captcha token 失败: %s", d.accountErr(out.ErrorDesc, out.Error, resp))
	}
	d.CaptchaToken = strings.TrimSpace(out.CaptchaToken)
	d.accountClient.SetHeader("X-Captcha-Token", d.CaptchaToken)
	op.MustSaveDriverStorage(d)
	return nil
}

// ========== GetRoot ==========

func (d *Guangya) GetRoot(ctx context.Context) (model.Obj, error) {
	rootID, err := d.getRootFolderID(ctx)
	if err != nil {
		return nil, err
	}
	return &model.Object{
		ID:       rootID,
		Path:     "/",
		Name:     "root",
		Size:     0,
		Modified: d.Modified,
		IsFolder: true,
	}, nil
}

// ========== List ==========

func (d *Guangya) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if err := d.ensureAccessToken(ctx); err != nil {
		return nil, err
	}

	parentID := dir.GetID()

	res := make([]model.Obj, 0, d.PageSize)
	for page := 0; ; page++ {
		var resp listResp
		body := map[string]any{
			"parentId":  parentID,
			"page":      page,
			"pageSize":  d.PageSize,
			"orderBy":   d.OrderBy,
			"sortType":  d.SortType,
			"fileTypes": []int{},
		}
		if err := d.postAPI(ctx, "/userres/v1/file/get_file_list", body, &resp); err != nil {
			return nil, err
		}
		for _, item := range resp.Data.List {
			res = append(res, &model.Object{
				ID:       item.FileID,
				Path:     parentID,
				Name:     item.FileName,
				Size:     item.FileSize,
				Modified: unixOrZero(item.UTime),
				Ctime:    unixOrZero(item.CTime),
				IsFolder: item.ResType == 2,
			})
		}
		if len(resp.Data.List) < d.PageSize {
			break
		}
		if resp.Data.Total > 0 && len(res) >= resp.Data.Total {
			break
		}
	}
	return res, nil
}

// ========== Link ==========

func (d *Guangya) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if file.IsDir() {
		return nil, errs.NotFile
	}
	if err := d.ensureAccessToken(ctx); err != nil {
		return nil, err
	}

	var resp downloadResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/get_res_download_url", map[string]any{
		"fileId": file.GetID(),
	}, &resp); err != nil {
		return nil, err
	}

	url := strings.TrimSpace(resp.Data.SignedURL)
	if url == "" {
		url = strings.TrimSpace(resp.Data.DownloadURL)
	}
	if url == "" {
		return nil, errors.New("empty download url")
	}
	return &model.Link{URL: url}, nil
}

// ========== Access Token Helper ==========

func (d *Guangya) ensureAccessToken(ctx context.Context) error {
	if d.AccessToken != "" {
		return nil
	}
	if d.RefreshToken != "" {
		if err := d.refreshToken(ctx); err != nil {
			return err
		}
		if d.AccessToken != "" {
			return nil
		}
	}
	if d.canSMSLogin() {
		if err := d.loginBySMSCode(ctx); err != nil {
			return err
		}
		if d.AccessToken != "" {
			return nil
		}
	}
	return errors.New("access token is empty, please login first")
}

// ========== Root Folder ==========

func (d *Guangya) getRootFolderID(ctx context.Context) (string, error) {
	if d.rootFolderResolved {
		return d.resolvedRootFolderID, nil
	}
	if err := d.ensureAccessToken(ctx); err != nil {
		return "", err
	}
	if err := d.prepareRootFolder(ctx); err != nil {
		return "", err
	}
	return d.resolvedRootFolderID, nil
}

func (d *Guangya) prepareRootFolder(ctx context.Context) error {
	rootID, err := d.resolveConfiguredRootFolderID(ctx)
	if err != nil {
		return err
	}
	d.resolvedRootFolderID = rootID
	d.rootFolderResolved = true
	return nil
}

func (d *Guangya) resolveConfiguredRootFolderID(ctx context.Context) (string, error) {
	root := strings.TrimSpace(d.RootPath)
	if root == "" {
		return "", nil
	}
	return d.resolveFolderPath(ctx, root)
}

func (d *Guangya) resolveFolderPath(ctx context.Context, rootPath string) (string, error) {
	cleanPath := strings.Trim(strings.ReplaceAll(strings.TrimSpace(rootPath), "\\", "/"), "/")
	if cleanPath == "" {
		return "", nil
	}

	parentID := ""
	for _, name := range strings.Split(cleanPath, "/") {
		if name == "" {
			continue
		}
		childID, err := d.findChildFolderID(ctx, parentID, name)
		if err != nil {
			return "", err
		}
		parentID = childID
	}
	return parentID, nil
}

func (d *Guangya) findChildFolderID(ctx context.Context, parentID, name string) (string, error) {
	for page := 0; ; page++ {
		var resp listResp
		body := map[string]any{
			"parentId": parentID,
			"page":     page,
			"pageSize": d.PageSize,
			"orderBy":  3,
			"sortType": 1,
		}
		if err := d.postAPI(ctx, "/userres/v1/file/get_file_list", body, &resp); err != nil {
			return "", err
		}
		for _, item := range resp.Data.List {
			if item.ResType == 2 && item.FileName == name {
				return item.FileID, nil
			}
		}
		if len(resp.Data.List) < d.PageSize {
			break
		}
		if resp.Data.Total > 0 && (page+1)*d.PageSize >= resp.Data.Total {
			break
		}
	}
	return "", fmt.Errorf("folder not found: %s", name)
}

// ========== API Request Helper ==========

func (d *Guangya) postAPI(ctx context.Context, path string, body any, out any) error {
	if strings.TrimSpace(d.AccessToken) == "" {
		return errors.New("access token is empty")
	}
	if err := d.apiRateLimitWait(ctx, path); err != nil {
		return err
	}
	resp, err := d.apiClient.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetBody(body).
		SetResult(out).
		Post(path)
	if err != nil {
		return err
	}
	if resp.StatusCode() == 401 || resp.StatusCode() == 403 {
		if strings.TrimSpace(d.RefreshToken) == "" {
			return fmt.Errorf("request failed: status=%d body=%s", resp.StatusCode(), resp.String())
		}
		if err := d.refreshToken(ctx); err != nil {
			return err
		}
		resp, err = d.apiClient.R().
			SetContext(ctx).
			SetHeader("Authorization", "Bearer "+d.AccessToken).
			SetBody(body).
			SetResult(out).
			Post(path)
		if err != nil {
			return err
		}
	}
	if resp.IsError() {
		return fmt.Errorf("request failed: status=%d body=%s", resp.StatusCode(), resp.String())
	}
	return nil
}

func (d *Guangya) apiRateLimitWait(ctx context.Context, path string) error {
	value, _ := d.apiRateLimit.LoadOrStore(path, rate.NewLimiter(rate.Every(apiRateInterval), 1))
	return value.(*rate.Limiter).Wait(ctx)
}

// ========== Status & Error Helpers ==========

func (d *Guangya) setTempStatus(status string) {
	time.AfterFunc(200*time.Millisecond, func() {
		d.GetStorage().SetStatus(status)
		op.MustSaveDriverStorage(d)
	})
}

func (d *Guangya) accountErr(desc, short string, resp *resty.Response) string {
	msg := strings.TrimSpace(desc)
	if msg == "" {
		msg = strings.TrimSpace(short)
	}
	if msg == "" && resp != nil {
		msg = strings.TrimSpace(resp.String())
	}
	if msg == "" && resp != nil {
		msg = fmt.Sprintf("status=%d", resp.StatusCode())
	}
	if msg == "" {
		msg = "unknown error"
	}
	return msg
}

// ========== Phone / Device Helpers ==========

func normalizePhoneE164(phone string) string {
	p := strings.TrimSpace(phone)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, " ", "")
	if strings.HasPrefix(p, "+") {
		if strings.HasPrefix(p, "+86") && len(p) > 3 {
			rest := strings.TrimPrefix(p, "+86")
			return "+86 " + rest
		}
		return p
	}
	digits := normalizeCaptchaUsername(p)
	if len(digits) == 11 {
		return "+86 " + digits
	}
	return p
}

func normalizeCaptchaUsername(phone string) string {
	p := strings.TrimSpace(phone)
	p = strings.ReplaceAll(p, " ", "")
	p = strings.TrimPrefix(p, "+")
	b := make([]rune, 0, len(p))
	for _, ch := range p {
		if ch >= '0' && ch <= '9' {
			b = append(b, ch)
		}
	}
	digits := string(b)
	if strings.HasPrefix(digits, "86") && len(digits) > 11 {
		digits = digits[2:]
	}
	return digits
}

func normalizeDeviceID(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "-", "")
	if len(v) != 32 {
		return ""
	}
	for _, ch := range v {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return ""
		}
	}
	return v
}

func randomDeviceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "0123456789abcdef0123456789abcdef"
	}
	return hex.EncodeToString(b)
}

// Compile-time check
var _ driver.Driver = (*Guangya)(nil)
var _ driver.GetRooter = (*Guangya)(nil)
