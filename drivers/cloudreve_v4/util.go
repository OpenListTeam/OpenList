package cloudreve_v4

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
	"github.com/go-resty/resty/v2"
	jsoniter "github.com/json-iterator/go"
)

// do others that not defined in Driver interface

const (
	CodeLoginRequired     = http.StatusUnauthorized
	CodePathNotExist      = 40016 // Path not exist
	CodeCredentialInvalid = 40020 // Failed to issue token
)

var (
	ErrorIssueToken = errors.New("failed to issue token")
)

func (d *CloudreveV4) getUA() string {
	if d.CustomUA != "" {
		return d.CustomUA
	}
	return base.UserAgent
}

func (d *CloudreveV4) request(method string, path string, callback base.ReqCallback, out any) error {
	if d.ref != nil {
		return d.ref.request(method, path, callback, out)
	}

	// ensure token
	if d.isTokenExpired() {
		err := d.refreshToken()
		if err != nil {
			return err
		}
	}

	return d._request(method, path, callback, out)
}

func (d *CloudreveV4) _request(method string, path string, callback base.ReqCallback, out any) error {
	if d.ref != nil {
		return d.ref._request(method, path, callback, out)
	}

	u := d.Address + "/api/v4" + path
	req := base.RestyClient.R()
	req.SetHeaders(map[string]string{
		"Accept":     "application/json, text/plain, */*",
		"User-Agent": d.getUA(),
	})
	if d.AccessToken != "" {
		req.SetHeader("Authorization", "Bearer "+d.AccessToken)
	}

	var r Resp
	req.SetResult(&r)

	if callback != nil {
		callback(req)
	}

	resp, err := req.Execute(method, u)
	if err != nil {
		return err
	}
	if !resp.IsSuccess() {
		return errors.New(resp.String())
	}

	if r.Code != 0 {
		if r.Code == CodeLoginRequired && d.canLogin() && path != "/session/token/refresh" {
			err = d.login()
			if err != nil {
				return err
			}
			return d.request(method, path, callback, out)
		}
		if r.Code == CodeCredentialInvalid {
			return ErrorIssueToken
		}
		if r.Code == CodePathNotExist {
			return errs.ObjectNotFound
		}
		return fmt.Errorf("%d: %s", r.Code, r.Msg)
	}

	if out != nil && r.Data != nil {
		var marshal []byte
		marshal, err = json.Marshal(r.Data)
		if err != nil {
			return err
		}
		err = json.Unmarshal(marshal, out)
		if err != nil {
			return err
		}
	}

	return nil
}

func (d *CloudreveV4) canLogin() bool {
	return d.Username != "" && d.Password != ""
}

func (d *CloudreveV4) login() error {
	var siteConfig SiteLoginConfigResp
	err := d._request(http.MethodGet, "/site/config/login", nil, &siteConfig)
	if err != nil {
		return err
	}
	var prepareLogin PrepareLoginResp
	err = d._request(http.MethodGet, "/session/prepare?email="+d.Addition.Username, nil, &prepareLogin)
	if err != nil {
		return err
	}
	if !prepareLogin.PasswordEnabled {
		return errors.New("password not enabled")
	}
	if prepareLogin.WebauthnEnabled {
		return errors.New("webauthn not support")
	}
	for range 5 {
		err = d.doLogin(siteConfig.LoginCaptcha)
		if err == nil {
			break
		}
		if err.Error() != "CAPTCHA not match." {
			break
		}
	}
	return err
}

func (d *CloudreveV4) doLogin(needCaptcha bool) error {
	var err error
	loginBody := base.Json{
		"email":    d.Username,
		"password": d.Password,
	}
	if needCaptcha {
		var config BasicConfigResp
		err = d._request(http.MethodGet, "/site/config/basic", nil, &config)
		if err != nil {
			return err
		}
		if config.CaptchaType != "normal" {
			return fmt.Errorf("captcha type %s not support", config.CaptchaType)
		}
		var captcha CaptchaResp
		err = d._request(http.MethodGet, "/site/captcha", nil, &captcha)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(captcha.Image, "data:image/png;base64,") {
			return errors.New("can not get captcha")
		}
		loginBody["ticket"] = captcha.Ticket
		i := strings.Index(captcha.Image, ",")
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(captcha.Image[i+1:]))
		vRes, err := base.RestyClient.R().SetMultipartField(
			"image", "validateCode.png", "image/png", dec).
			Post(setting.GetStr(conf.OcrApi))
		if err != nil {
			return err
		}
		if jsoniter.Get(vRes.Body(), "status").ToInt() != 200 {
			return errors.New("ocr error:" + jsoniter.Get(vRes.Body(), "msg").ToString())
		}
		captchaCode := jsoniter.Get(vRes.Body(), "result").ToString()
		if captchaCode == "" {
			return errors.New("ocr error: empty result")
		}
		loginBody["captcha"] = captchaCode
	}
	var token TokenResponse
	err = d._request(http.MethodPost, "/session/token", func(req *resty.Request) {
		req.SetBody(loginBody)
	}, &token)
	if err != nil {
		return err
	}
	d.AccessToken, d.RefreshToken = token.Token.AccessToken, token.Token.RefreshToken
	d.AccessExpires, d.RefreshExpires = token.Token.AccessExpires, token.Token.RefreshExpires
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *CloudreveV4) refreshToken() error {
	// if no refresh token, try to login if possible
	if d.RefreshToken == "" {
		if d.canLogin() {
			err := d.login()
			if err != nil {
				return fmt.Errorf("cannot login to get refresh token, error: %s", err)
			}
		}
		return nil
	}

	// parse jwt to check if refresh token is valid
	var jwt RefreshJWT
	err := d.parseJWT(d.RefreshToken, &jwt)
	if err != nil {
		// if refresh token is invalid, try to login if possible
		if d.canLogin() {
			return d.login()
		}
		d.GetStorage().SetStatus(fmt.Sprintf("Invalid RefreshToken: %s", err.Error()))
		op.MustSaveDriverStorage(d)
		return fmt.Errorf("invalid refresh token: %w", err)
	}

	// do refresh token
	var token Token
	err = d._request(http.MethodPost, "/session/token/refresh", func(req *resty.Request) {
		req.SetBody(base.Json{
			"refresh_token": d.RefreshToken,
		})
	}, &token)
	if err != nil {
		if errors.Is(err, ErrorIssueToken) {
			if d.canLogin() {
				// try to login again
				return d.login()
			}
			d.GetStorage().SetStatus("This session is no longer valid")
			op.MustSaveDriverStorage(d)
			return ErrorIssueToken
		}
		return err
	}
	d.AccessToken, d.RefreshToken = token.AccessToken, token.RefreshToken
	d.AccessExpires, d.RefreshExpires = token.AccessExpires, token.RefreshExpires
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *CloudreveV4) parseJWT(token string, jwt any) error {
	split := strings.Split(token, ".")
	if len(split) != 3 {
		return fmt.Errorf("invalid token length: %d, ensure the token is a valid JWT", len(split))
	}
	data, err := base64.RawURLEncoding.DecodeString(split[1])
	if err != nil {
		return fmt.Errorf("invalid token encoding: %w, ensure the token is a valid JWT", err)
	}
	err = json.Unmarshal(data, &jwt)
	if err != nil {
		return fmt.Errorf("invalid token content: %w, ensure the token is a valid JWT", err)
	}
	return nil
}

// check if token is expired
// https://github.com/cloudreve/frontend/blob/ddfacc1c31c49be03beb71de4cc114c8811038d6/src/session/index.ts#L177-L200
func (d *CloudreveV4) isTokenExpired() bool {
	if d.RefreshToken == "" {
		// login again if username and password is set
		if d.canLogin() {
			return true
		}
		// no refresh token, cannot refresh
		return false
	}
	if d.AccessToken == "" {
		return true
	}
	var (
		err     error
		expires time.Time
	)
	// check if token is expired
	if d.AccessExpires != "" {
		// use expires field if possible to prevent timezone issue
		// only available after login or refresh token
		// 2025-08-28T02:43:07.645109985+08:00
		expires, err = time.Parse(time.RFC3339Nano, d.AccessExpires)
		if err != nil {
			return false
		}
	} else {
		// fallback to parse jwt
		// if failed, disable the storage
		var jwt AccessJWT
		err = d.parseJWT(d.AccessToken, &jwt)
		if err != nil {
			d.GetStorage().SetStatus(fmt.Sprintf("Invalid AccessToken: %s", err.Error()))
			op.MustSaveDriverStorage(d)
			return false
		}
		// may be have timezone issue
		expires = time.Unix(jwt.Exp, 0)
	}
	// add a 10 minutes safe margin
	ddl := time.Now().Add(10 * time.Minute)
	if expires.Before(ddl) {
		// current access token expired, check if refresh token is expired
		// warning: cannot parse refresh token from jwt, because the exp field is not standard
		if d.RefreshExpires != "" {
			refreshExpires, err := time.Parse(time.RFC3339Nano, d.RefreshExpires)
			if err != nil {
				return false
			}
			if refreshExpires.Before(time.Now()) {
				// This session is no longer valid
				if d.canLogin() {
					// try to login again
					return true
				}
				d.GetStorage().SetStatus("This session is no longer valid")
				op.MustSaveDriverStorage(d)
				return false
			}
		}
		return true
	}
	return false
}

func (d *CloudreveV4) upLocal(ctx context.Context, file model.FileStreamer, u FileUploadResp, up driver.UpdateProgress) error {
	var finish int64 = 0
	var chunk int = 0
	DEFAULT := int64(u.ChunkSize)
	if DEFAULT == 0 {
		// support relay
		DEFAULT = file.GetSize()
	}
	for finish < file.GetSize() {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		left := file.GetSize() - finish
		byteSize := min(left, DEFAULT)
		utils.Log.Debugf("[CloudreveV4-Local] upload range: %d-%d/%d", finish, finish+byteSize-1, file.GetSize())
		byteData := make([]byte, byteSize)
		n, err := io.ReadFull(file, byteData)
		utils.Log.Debug(err, n)
		if err != nil {
			return err
		}
		err = d.request(http.MethodPost, "/file/upload/"+u.SessionID+"/"+strconv.Itoa(chunk), func(req *resty.Request) {
			req.SetHeader("Content-Type", "application/octet-stream")
			req.SetContentLength(true)
			req.SetHeader("Content-Length", strconv.FormatInt(byteSize, 10))
			req.SetBody(driver.NewLimitedUploadStream(ctx, bytes.NewReader(byteData)))
			req.AddRetryCondition(func(r *resty.Response, err error) bool {
				if err != nil {
					return true
				}
				if r.IsError() {
					return true
				}
				var retryResp Resp
				jErr := base.RestyClient.JSONUnmarshal(r.Body(), &retryResp)
				if jErr != nil {
					return true
				}
				if retryResp.Code != 0 {
					return true
				}
				return false
			})
		}, nil)
		if err != nil {
			return err
		}
		finish += byteSize
		up(float64(finish) * 100 / float64(file.GetSize()))
		chunk++
	}
	return nil
}

func (d *CloudreveV4) upRemote(ctx context.Context, file model.FileStreamer, u FileUploadResp, up driver.UpdateProgress) error {
	DEFAULT := int64(u.ChunkSize)
	ss, err := stream.NewStreamSectionReader(file, int(DEFAULT), &up)
	if err != nil {
		return err
	}

	uploadUrl := u.UploadUrls[0]
	credential := u.Credential
	var finish int64 = 0
	var chunk int = 0
	for finish < file.GetSize() {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		left := file.GetSize() - finish
		byteSize := min(left, DEFAULT)
		utils.Log.Debugf("[CloudreveV4-Remote] upload range: %d-%d/%d", finish, finish+byteSize-1, file.GetSize())
		rd, err := ss.GetSectionReader(finish, byteSize)
		if err != nil {
			return err
		}
		err = retry.Do(
			func() error {
				rd.Seek(0, io.SeekStart)
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadUrl+"?chunk="+strconv.Itoa(chunk),
					driver.NewLimitedUploadStream(ctx, rd))
				if err != nil {
					return err
				}

				req.ContentLength = byteSize
				req.Header.Set("Authorization", fmt.Sprint(credential))
				req.Header.Set("User-Agent", d.getUA())
				res, err := base.HttpClient.Do(req)
				if err != nil {
					return err
				}
				defer res.Body.Close()
				if res.StatusCode != 200 {
					return fmt.Errorf("server error: %d", res.StatusCode)
				}
				body, err := io.ReadAll(res.Body)
				if err != nil {
					return err
				}
				var up Resp
				err = json.Unmarshal(body, &up)
				if err != nil {
					return err
				}
				if up.Code != 0 {
					return errors.New(up.Msg)
				}
				return nil
			},
			retry.Context(ctx),
			retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second),
		)
		ss.FreeSectionReader(rd)
		if err != nil {
			return err
		}
		finish += byteSize
		up(float64(finish) * 100 / float64(file.GetSize()))
		chunk++
	}
	return nil
}

func (d *CloudreveV4) upOneDrive(ctx context.Context, file model.FileStreamer, u FileUploadResp, up driver.UpdateProgress) error {
	DEFAULT := int64(u.ChunkSize)
	ss, err := stream.NewStreamSectionReader(file, int(DEFAULT), &up)
	if err != nil {
		return err
	}

	uploadUrl := u.UploadUrls[0]
	var finish int64 = 0
	for finish < file.GetSize() {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		left := file.GetSize() - finish
		byteSize := min(left, DEFAULT)
		utils.Log.Debugf("[CloudreveV4-OneDrive] upload range: %d-%d/%d", finish, finish+byteSize-1, file.GetSize())
		rd, err := ss.GetSectionReader(finish, byteSize)
		if err != nil {
			return err
		}
		err = retry.Do(
			func() error {
				rd.Seek(0, io.SeekStart)
				req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadUrl, driver.NewLimitedUploadStream(ctx, rd))
				if err != nil {
					return err
				}

				req.ContentLength = byteSize
				req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", finish, finish+byteSize-1, file.GetSize()))
				req.Header.Set("User-Agent", d.getUA())
				res, err := base.HttpClient.Do(req)
				if err != nil {
					return err
				}
				defer res.Body.Close()
				// https://learn.microsoft.com/zh-cn/onedrive/developer/rest-api/api/driveitem_createuploadsession
				switch {
				case res.StatusCode >= 500 && res.StatusCode <= 504:
					return fmt.Errorf("server error: %d", res.StatusCode)
				case res.StatusCode != 201 && res.StatusCode != 202 && res.StatusCode != 200:
					data, _ := io.ReadAll(res.Body)
					return errors.New(string(data))
				default:
					return nil
				}
			},
			retry.Context(ctx),
			retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second),
		)
		ss.FreeSectionReader(rd)
		if err != nil {
			return err
		}
		finish += byteSize
		up(float64(finish) * 100 / float64(file.GetSize()))
	}
	// 上传成功发送回调请求
	return d.request(http.MethodPost, "/callback/onedrive/"+u.SessionID+"/"+u.CallbackSecret, func(req *resty.Request) {
		req.SetBody("{}")
	}, nil)
}

func (d *CloudreveV4) upS3(ctx context.Context, file model.FileStreamer, u FileUploadResp, up driver.UpdateProgress, s3Type string) error {
	DEFAULT := int64(u.ChunkSize)
	ss, err := stream.NewStreamSectionReader(file, int(DEFAULT), &up)
	if err != nil {
		return err
	}

	var finish int64 = 0
	var chunk int = 0
	var etags []string
	for finish < file.GetSize() {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		left := file.GetSize() - finish
		byteSize := min(left, DEFAULT)
		utils.Log.Debugf("[CloudreveV4-S3] upload range: %d-%d/%d", finish, finish+byteSize-1, file.GetSize())
		rd, err := ss.GetSectionReader(finish, byteSize)
		if err != nil {
			return err
		}
		err = retry.Do(
			func() error {
				rd.Seek(0, io.SeekStart)
				req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.UploadUrls[chunk],
					driver.NewLimitedUploadStream(ctx, rd))
				if err != nil {
					return err
				}
				req.ContentLength = byteSize
				req.Header.Set("User-Agent", d.getUA())
				if s3Type == "ks3" {
					req.Header.Set("Content-Type", "application/octet-stream")
				}
				res, err := base.HttpClient.Do(req)
				if err != nil {
					return err
				}
				etag := res.Header.Get("ETag")
				res.Body.Close()
				switch {
				case res.StatusCode != 200:
					return fmt.Errorf("server error: %d", res.StatusCode)
				case etag == "":
					return errors.New("failed to get ETag from header")
				default:
					etags = append(etags, etag)
					return nil
				}
			},
			retry.Context(ctx),
			retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second),
		)
		ss.FreeSectionReader(rd)
		if err != nil {
			return err
		}
		finish += byteSize
		up(float64(finish) * 100 / float64(file.GetSize()))
		chunk++
	}

	// s3LikeFinishUpload
	bodyBuilder := &strings.Builder{}
	bodyBuilder.WriteString("<CompleteMultipartUpload>")
	for i, etag := range etags {
		bodyBuilder.WriteString(fmt.Sprintf(
			`<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>`,
			i+1, // PartNumber 从 1 开始
			etag,
		))
	}
	bodyBuilder.WriteString("</CompleteMultipartUpload>")
	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost,
		u.CompleteURL,
		strings.NewReader(bodyBuilder.String()),
	)
	if err != nil {
		return err
	}
	if s3Type == "ks3" {
		req.Header.Set("Content-Type", "application/octet-stream")
	} else {
		req.Header.Set("Content-Type", "application/xml")
	}
	req.Header.Set("User-Agent", d.getUA())
	res, err := base.HttpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("up status: %d, error: %s", res.StatusCode, string(body))
	}

	// 上传成功发送回调请求
	return d.request(http.MethodGet, "/callback/"+s3Type+"/"+u.SessionID+"/"+u.CallbackSecret, nil, nil)
}
