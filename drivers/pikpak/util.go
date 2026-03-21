package pikpak

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	netutil "github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/go-resty/resty/v2"
	jsoniter "github.com/json-iterator/go"
	"github.com/pkg/errors"
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
	OSSUserAgent               = "aliyun-sdk-android/2.9.13(Linux/Android 14/M2004j7ac;UKQ1.231108.001)"
	OssSecurityTokenHeaderName = "X-OSS-Security-Token"
	ThreadsNum                 = 10
)

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

func (d *PikPak) loginRaw() error {
	// 检查用户名和密码是否为空
	if d.Addition.Username == "" || d.Addition.Password == "" {
		return errors.New("username or password is empty")
	}

	url := "https://user.mypikpak.net/v1/auth/signin"
	action := GetAction(http.MethodPost, url)
	if !d.HasValidCaptchaToken() {
		if err := d.RefreshCaptchaTokenInLogin(action, d.Username); err != nil {
			return err
		}
	}

	doLogin := func() error {
		var e ErrResp
		res, err := base.RestyClient.SetRetryCount(1).R().SetError(&e).SetBody(base.Json{
			"captcha_token": d.GetCaptchaToken(),
			"client_id":     d.ClientID,
			"client_secret": d.ClientSecret,
			"username":      d.Username,
			"password":      d.Password,
		}).SetQueryParam("client_id", d.ClientID).Post(url)
		if err != nil {
			return err
		}
		if e.ErrorCode != 0 {
			return &e
		}
		data := res.Body()
		refreshToken := jsoniter.Get(data, "refresh_token").ToString()
		accessToken := jsoniter.Get(data, "access_token").ToString()
		d.Common.SetUserID(jsoniter.Get(data, "sub").ToString())
		d.setAuthTokens(accessToken, refreshToken)
		d.saveStorage(func() {
			d.Addition.RefreshToken = refreshToken
		})
		return nil
	}

	err := doLogin()
	if apiErr, ok := err.(*ErrResp); ok && apiErr.ErrorCode == 9 {
		d.Common.SetCaptchaExpiry(time.Time{})
		d.Common.SetCaptchaToken("")
		if err = d.RefreshCaptchaTokenInLogin(action, d.Username); err != nil {
			return err
		}
		return doLogin()
	}
	return err
}

func (d *PikPak) refreshTokenRaw(refreshToken string) error {
	url := "https://user.mypikpak.net/v1/auth/token"
	var e ErrResp
	res, err := base.RestyClient.SetRetryCount(1).R().SetError(&e).
		SetHeader("user-agent", "").SetBody(base.Json{
		"client_id":     d.ClientID,
		"client_secret": d.ClientSecret,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	}).SetQueryParam("client_id", d.ClientID).Post(url)
	if err != nil {
		d.saveStorage(func() {
			d.Status = err.Error()
		})
		return err
	}
	if e.ErrorCode != 0 {
		if e.ErrorCode == 4126 {
			// 1. 未填写 username 或 password
			if d.Addition.Username == "" || d.Addition.Password == "" {
				return errors.New("refresh_token invalid, please re-provide refresh_token")
			} else {
				// refresh_token invalid, re-login
				return d.loginRaw()
			}
		}
		d.saveStorage(func() {
			d.Status = e.Error()
		})
		return errors.New(e.Error())
	}
	data := res.Body()
	refreshToken = jsoniter.Get(data, "refresh_token").ToString()
	accessToken := jsoniter.Get(data, "access_token").ToString()
	d.Common.SetUserID(jsoniter.Get(data, "sub").ToString())
	d.setAuthTokens(accessToken, refreshToken)
	d.saveStorage(func() {
		d.Status = "work"
		d.Addition.RefreshToken = refreshToken
	})
	return nil
}

func (d *PikPak) authorizeRaw() error {
	if refreshToken := d.getRefreshToken(); refreshToken != "" {
		return d.refreshTokenRaw(refreshToken)
	}
	return d.loginRaw()
}

func (d *PikPak) ensureAuthorized(force bool, staleAccessToken string) error {
	if !force && d.getAccessToken() != "" {
		return nil
	}
	if force && staleAccessToken != "" {
		if currentAccessToken := d.getAccessToken(); currentAccessToken != "" && currentAccessToken != staleAccessToken {
			return nil
		}
	}

	_, err, _ := d.authG.Do("auth", func() (struct{}, error) {
		if !force && d.getAccessToken() != "" {
			return struct{}{}, nil
		}
		if force && staleAccessToken != "" {
			if currentAccessToken := d.getAccessToken(); currentAccessToken != "" && currentAccessToken != staleAccessToken {
				return struct{}{}, nil
			}
		}
		return struct{}{}, d.authorizeRaw()
	})
	return err
}

func (d *PikPak) request(url string, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
	if err := d.ensureAuthorized(false, ""); err != nil {
		return nil, err
	}
	if !d.HasValidCaptchaToken() {
		if err := d.RefreshCaptchaTokenAtLogin(GetAction(method, url), d.GetUserID()); err != nil {
			return nil, err
		}
	}

	req := base.RestyClient.R()
	req.SetHeaders(map[string]string{
		//"Authorization":   "Bearer " + d.AccessToken,
		"User-Agent":      d.GetUserAgent(),
		"X-Device-ID":     d.GetDeviceID(),
		"X-Captcha-Token": d.GetCaptchaToken(),
	})
	accessToken := d.getAccessToken()
	if accessToken != "" {
		req.SetHeader("Authorization", "Bearer "+accessToken)
	}

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
	case 4122, 4121, 16:
		// access_token 过期
		if err1 := d.ensureAuthorized(true, accessToken); err1 != nil {
			return nil, err1
		}
		return d.request(url, method, callback, resp)
	case 9: // 验证码token过期
		d.Common.SetCaptchaExpiry(time.Time{})
		d.Common.SetCaptchaToken("")
		if err = d.RefreshCaptchaTokenAtLogin(GetAction(method, url), d.GetUserID()); err != nil {
			return nil, err
		}
		return d.request(url, method, callback, resp)
	case 10: // 操作频繁
		return nil, errors.New(e.ErrorDescription)
	default:
		return nil, errors.New(e.Error())
	}
}

func (d *PikPak) getFiles(id string) ([]File, error) {
	res := make([]File, 0)
	pageToken := "first"
	for pageToken != "" {
		if pageToken == "first" {
			pageToken = ""
		}
		query := map[string]string{
			"parent_id":      id,
			"thumbnail_size": "SIZE_LARGE",
			"with_audit":     "true",
			"limit":          "100",
			"filters":        `{"phase":{"eq":"PHASE_TYPE_COMPLETE"},"trashed":{"eq":false}}`,
			"page_token":     pageToken,
		}
		var resp Files
		_, err := d.request("https://api-drive.mypikpak.net/drive/v1/files", http.MethodGet, func(req *resty.Request) {
			req.SetQueryParams(query)
		}, &resp)
		if err != nil {
			return nil, err
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
	UserID        string
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

func (c *Common) SetDeviceID(deviceID string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.DeviceID = deviceID
}

func (c *Common) SetUserID(userID string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.UserID = userID
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

func (c *Common) GetCaptchaToken() string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.CaptchaToken
}

func (c *Common) HasValidCaptchaToken() bool {
	token, expiry, _, _ := c.captchaSnapshot()
	return hasValidCaptchaToken(token, expiry)
}

func (c *Common) GetUserAgent() string {
	return c.UserAgent
}

func (c *Common) GetDeviceID() string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.DeviceID
}

func (c *Common) GetUserID() string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.UserID
}

func (c *Common) captchaSnapshot() (token string, expiry time.Time, deviceID, userID string) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.CaptchaToken, c.CaptchaExpiry, c.DeviceID, c.UserID
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

func (d *PikPak) getAccessToken() string {
	d.authMu.RLock()
	defer d.authMu.RUnlock()
	return d.AccessToken
}

func (d *PikPak) getRefreshToken() string {
	d.authMu.RLock()
	defer d.authMu.RUnlock()
	return d.RefreshToken
}

func (d *PikPak) authSnapshot() (accessToken, refreshToken string) {
	d.authMu.RLock()
	defer d.authMu.RUnlock()
	return d.AccessToken, d.RefreshToken
}

func (d *PikPak) setRefreshTokenState(refreshToken string) {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	d.RefreshToken = refreshToken
}

func (d *PikPak) setAuthTokens(accessToken, refreshToken string) {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	d.AccessToken = accessToken
	d.RefreshToken = refreshToken
}

// RefreshCaptchaTokenAtLogin 刷新验证码token(登录后)
func (d *PikPak) RefreshCaptchaTokenAtLogin(action, userID string) error {
	metas := map[string]string{
		"client_version": d.ClientVersion,
		"package_name":   d.PackageName,
		"user_id":        userID,
	}
	metas["timestamp"], metas["captcha_sign"] = d.Common.GetCaptchaSign()
	return d.refreshCaptchaToken(action, metas, true)
}

// RefreshCaptchaTokenInLogin 刷新验证码token(登录时)
func (d *PikPak) RefreshCaptchaTokenInLogin(action, username string) error {
	metas := make(map[string]string)
	if ok, _ := regexp.MatchString(`\w+([-+.]\w+)*@\w+([-.]\w+)*\.\w+([-.]\w+)*`, username); ok {
		metas["email"] = username
	} else if len(username) >= 11 && len(username) <= 18 {
		metas["phone_number"] = username
	} else {
		metas["username"] = username
	}
	return d.refreshCaptchaToken(action, metas, false)
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

func isAuthExpiredErrorCode(code int64) bool {
	return code == 4122 || code == 4121 || code == 16
}

func (d *PikPak) initCaptchaToken(action string, metas map[string]string, oldToken, deviceID, accessToken string) (ErrResp, CaptchaTokenResponse, error) {
	e := ErrResp{}
	resp := CaptchaTokenResponse{}
	param := CaptchaTokenRequest{
		Action:       action,
		CaptchaToken: oldToken,
		ClientID:     d.ClientID,
		DeviceID:     deviceID,
		Meta:         metas,
		RedirectUri:  "xlaccsdk01://xbase.cloud/callback?state=harbor",
	}
	req := base.RestyClient.R().
		SetHeaders(map[string]string{
			"User-Agent":  d.GetUserAgent(),
			"X-Device-ID": deviceID,
		}).
		SetError(&e).
		SetResult(&resp).
		SetBody(param).
		SetQueryParam("client_id", d.ClientID)
	if accessToken != "" {
		req.SetHeader("Authorization", "Bearer "+accessToken)
	}
	_, err := req.Execute(http.MethodPost, "https://user.mypikpak.net/v1/shield/captcha/init")
	return e, resp, err
}

func (d *PikPak) repairAuthorizationForCaptcha(accessToken string) error {
	d.Common.refreshMu.Unlock()
	defer d.Common.refreshMu.Lock()
	return d.ensureAuthorized(true, accessToken)
}

// refreshCaptchaToken 刷新CaptchaToken
func (d *PikPak) refreshCaptchaToken(action string, metas map[string]string, allowAuthRepair bool) error {
	d.Common.refreshMu.Lock()
	defer d.Common.refreshMu.Unlock()

	oldToken, expiry, deviceID, _ := d.Common.captchaSnapshot()
	if hasValidCaptchaToken(oldToken, expiry) {
		return nil
	}

	accessToken := ""
	if allowAuthRepair {
		accessToken = d.getAccessToken()
	}
	e, resp, err := d.initCaptchaToken(action, metas, oldToken, deviceID, accessToken)
	if err != nil {
		return err
	}
	if allowAuthRepair && isAuthExpiredErrorCode(e.ErrorCode) {
		if err = d.repairAuthorizationForCaptcha(accessToken); err != nil {
			return err
		}

		oldToken, expiry, deviceID, _ = d.Common.captchaSnapshot()
		if hasValidCaptchaToken(oldToken, expiry) {
			return nil
		}

		accessToken = d.getAccessToken()
		e, resp, err = d.initCaptchaToken(action, metas, "", deviceID, accessToken)
		if err != nil {
			return err
		}
	}

	if e.IsError() {
		return errors.New(e.Error())
	}

	if resp.Url != "" {
		return fmt.Errorf(`need verify: <a target="_blank" href="%s">Click Here</a>`, resp.Url)
	}

	d.Common.setCaptchaState(resp.CaptchaToken, resp.Expiry())
	refreshCTokenCk := d.Common.RefreshCTokenCk
	if refreshCTokenCk != nil {
		refreshCTokenCk(resp.CaptchaToken)
	}
	return nil
}

func (d *PikPak) UploadByOSS(ctx context.Context, params *S3Params, s model.FileStreamer, up driver.UpdateProgress) error {
	ossClient, err := netutil.NewOSSClient(params.Endpoint, params.AccessKeyID, params.AccessKeySecret)
	if err != nil {
		return err
	}
	bucket, err := ossClient.Bucket(params.Bucket)
	if err != nil {
		return err
	}

	err = bucket.PutObject(params.Key, driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
		Reader:         s,
		UpdateProgress: up,
	}), OssOption(params)...)
	if err != nil {
		return err
	}
	return nil
}

func (d *PikPak) UploadByMultipart(ctx context.Context, params *S3Params, fileSize int64, s model.FileStreamer, up driver.UpdateProgress) error {
	tmpF, err := s.CacheFullAndWriter(&up, nil)
	if err != nil {
		return err
	}

	var (
		chunks    []oss.FileChunk
		parts     []oss.UploadPart
		imur      oss.InitiateMultipartUploadResult
		ossClient *oss.Client
		bucket    *oss.Bucket
	)

	if ossClient, err = netutil.NewOSSClient(params.Endpoint, params.AccessKeyID, params.AccessKeySecret); err != nil {
		return err
	}

	if bucket, err = ossClient.Bucket(params.Bucket); err != nil {
		return err
	}

	ticker := time.NewTicker(time.Hour * 12)
	defer ticker.Stop()
	// 设置超时
	timeout := time.NewTimer(time.Hour * 24)

	if chunks, err = SplitFile(fileSize); err != nil {
		return err
	}

	if imur, err = bucket.InitiateMultipartUpload(params.Key,
		oss.SetHeader(OssSecurityTokenHeaderName, params.SecurityToken),
		oss.UserAgentHeader(OSSUserAgent),
	); err != nil {
		return err
	}

	wg := sync.WaitGroup{}
	wg.Add(len(chunks))

	chunksCh := make(chan oss.FileChunk)
	errCh := make(chan error)
	UploadedPartsCh := make(chan oss.UploadPart)
	quit := make(chan struct{})

	// producer
	go chunksProducer(chunksCh, chunks)
	go func() {
		wg.Wait()
		quit <- struct{}{}
	}()

	completedNum := atomic.Int32{}
	// consumers
	for i := 0; i < ThreadsNum; i++ {
		go func(threadId int) {
			defer func() {
				if r := recover(); r != nil {
					errCh <- fmt.Errorf("recovered in %v", r)
				}
			}()
			for chunk := range chunksCh {
				var part oss.UploadPart // 出现错误就继续尝试，共尝试3次
				for retry := 0; retry < 3; retry++ {
					select {
					case <-ctx.Done():
						break
					case <-ticker.C:
						errCh <- errors.Wrap(err, "ossToken 过期")
					default:
					}

					buf := make([]byte, chunk.Size)
					if _, err = tmpF.ReadAt(buf, chunk.Offset); err != nil && !errors.Is(err, io.EOF) {
						continue
					}

					b := driver.NewLimitedUploadStream(ctx, bytes.NewReader(buf))
					if part, err = bucket.UploadPart(imur, b, chunk.Size, chunk.Number, OssOption(params)...); err == nil {
						break
					}
				}
				if err != nil {
					errCh <- errors.Wrap(err, fmt.Sprintf("上传 %s 的第%d个分片时出现错误：%v", s.GetName(), chunk.Number, err))
				} else {
					num := completedNum.Add(1)
					up(float64(num) * 100.0 / float64(len(chunks)))
				}
				UploadedPartsCh <- part
			}
		}(i)
	}

	go func() {
		for part := range UploadedPartsCh {
			parts = append(parts, part)
			wg.Done()
		}
	}()
LOOP:
	for {
		select {
		case <-ticker.C:
			// ossToken 过期
			return err
		case <-quit:
			break LOOP
		case <-errCh:
			return err
		case <-timeout.C:
			return fmt.Errorf("time out")
		}
	}

	// EOF错误是xml的Unmarshal导致的，响应其实是json格式，所以实际上上传是成功的
	if _, err = bucket.CompleteMultipartUpload(imur, parts, OssOption(params)...); err != nil && !errors.Is(err, io.EOF) {
		// 当文件名含有 &< 这两个字符之一时响应的xml解析会出现错误，实际上上传是成功的
		if filename := filepath.Base(s.GetName()); !strings.ContainsAny(filename, "&<") {
			return err
		}
	}
	return nil
}

func chunksProducer(ch chan oss.FileChunk, chunks []oss.FileChunk) {
	for _, chunk := range chunks {
		ch <- chunk
	}
}

func SplitFile(fileSize int64) (chunks []oss.FileChunk, err error) {
	for i := int64(1); i < 10; i++ {
		if fileSize < i*utils.GB { // 文件大小小于iGB时分为i*100片
			if chunks, err = SplitFileByPartNum(fileSize, int(i*100)); err != nil {
				return
			}
			break
		}
	}
	if fileSize > 9*utils.GB { // 文件大小大于9GB时分为1000片
		if chunks, err = SplitFileByPartNum(fileSize, 1000); err != nil {
			return
		}
	}
	// 单个分片大小不能小于1MB
	if chunks[0].Size < 1*utils.MB {
		if chunks, err = SplitFileByPartSize(fileSize, 1*utils.MB); err != nil {
			return
		}
	}
	return
}

// SplitFileByPartNum splits big file into parts by the num of parts.
// Split the file with specified parts count, returns the split result when error is nil.
func SplitFileByPartNum(fileSize int64, chunkNum int) ([]oss.FileChunk, error) {
	if chunkNum <= 0 || chunkNum > 10000 {
		return nil, errors.New("chunkNum invalid")
	}

	if int64(chunkNum) > fileSize {
		return nil, errors.New("oss: chunkNum invalid")
	}

	var chunks []oss.FileChunk
	chunk := oss.FileChunk{}
	chunkN := (int64)(chunkNum)
	for i := int64(0); i < chunkN; i++ {
		chunk.Number = int(i + 1)
		chunk.Offset = i * (fileSize / chunkN)
		if i == chunkN-1 {
			chunk.Size = fileSize/chunkN + fileSize%chunkN
		} else {
			chunk.Size = fileSize / chunkN
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// SplitFileByPartSize splits big file into parts by the size of parts.
// Splits the file by the part size. Returns the FileChunk when error is nil.
func SplitFileByPartSize(fileSize int64, chunkSize int64) ([]oss.FileChunk, error) {
	if chunkSize <= 0 {
		return nil, errors.New("chunkSize invalid")
	}

	chunkN := fileSize / chunkSize
	if chunkN >= 10000 {
		return nil, errors.New("Too many parts, please increase part size")
	}

	var chunks []oss.FileChunk
	chunk := oss.FileChunk{}
	for i := int64(0); i < chunkN; i++ {
		chunk.Number = int(i + 1)
		chunk.Offset = i * chunkSize
		chunk.Size = chunkSize
		chunks = append(chunks, chunk)
	}

	if fileSize%chunkSize > 0 {
		chunk.Number = len(chunks) + 1
		chunk.Offset = int64(len(chunks)) * chunkSize
		chunk.Size = fileSize % chunkSize
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// OssOption get options
func OssOption(params *S3Params) []oss.Option {
	options := []oss.Option{
		oss.SetHeader(OssSecurityTokenHeaderName, params.SecurityToken),
		oss.UserAgentHeader(OSSUserAgent),
	}
	return options
}
