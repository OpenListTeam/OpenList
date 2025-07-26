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
		if r.Code == 401 && d.RefreshToken != "" && path != "/session/token/refresh" {
			// try to refresh token
			err = d.refreshToken()
			if err != nil {
				return err
			}
			return d.request(method, path, callback, out)
		}
		return errors.New(r.Msg)
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

func (d *CloudreveV4) login() error {
	var siteConfig SiteLoginConfigResp
	err := d.request(http.MethodGet, "/site/config/login", nil, &siteConfig)
	if err != nil {
		return err
	}
	var prepareLogin PrepareLoginResp
	err = d.request(http.MethodGet, "/session/prepare?email="+d.Addition.Username, nil, &prepareLogin)
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
		err = d.request(http.MethodGet, "/site/config/basic", nil, &config)
		if err != nil {
			return err
		}
		if config.CaptchaType != "normal" {
			return fmt.Errorf("captcha type %s not support", config.CaptchaType)
		}
		var captcha CaptchaResp
		err = d.request(http.MethodGet, "/site/captcha", nil, &captcha)
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
	err = d.request(http.MethodPost, "/session/token", func(req *resty.Request) {
		req.SetBody(loginBody)
	}, &token)
	if err != nil {
		return err
	}
	d.AccessToken, d.RefreshToken = token.Token.AccessToken, token.Token.RefreshToken
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *CloudreveV4) refreshToken() error {
	if d.RefreshToken == "" {
		if d.Username != "" {
			err := d.login()
			if err != nil {
				return fmt.Errorf("cannot login to get refresh token, error: %s", err)
			}
		}
		return nil
	}
	var token Token
	err := d.request(http.MethodPost, "/session/token/refresh", func(req *resty.Request) {
		req.SetBody(base.Json{
			"refresh_token": d.RefreshToken,
		})
	}, &token)
	if err != nil {
		return err
	}
	d.AccessToken, d.RefreshToken = token.AccessToken, token.RefreshToken
	op.MustSaveDriverStorage(d)
	return nil
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
	uploadUrl := u.UploadUrls[0]
	credential := u.Credential
	var finish int64 = 0
	var chunk int = 0
	DEFAULT := int64(u.ChunkSize)
	ss, err := stream.NewStreamSectionReader(file, int(DEFAULT))
	if err != nil {
		return err
	}
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
			}, retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second),
		)
		ss.RecycleSectionReader(rd)
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
	uploadUrl := u.UploadUrls[0]
	var finish int64 = 0
	DEFAULT := int64(u.ChunkSize)
	ss, err := stream.NewStreamSectionReader(file, int(DEFAULT))
	if err != nil {
		return err
	}
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
			}, retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second),
		)
		ss.RecycleSectionReader(rd)
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

func (d *CloudreveV4) upS3(ctx context.Context, file model.FileStreamer, u FileUploadResp, up driver.UpdateProgress) error {
	var finish int64 = 0
	var chunk int = 0
	var etags []string
	DEFAULT := int64(u.ChunkSize)
	ss, err := stream.NewStreamSectionReader(file, int(DEFAULT))
	if err != nil {
		return err
	}
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
			retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second),
		)
		ss.RecycleSectionReader(rd)
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
	req.Header.Set("Content-Type", "application/xml")
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
	return d.request(http.MethodGet, "/callback/s3/"+u.SessionID+"/"+u.CallbackSecret, func(req *resty.Request) {
		req.SetBody("{}")
	}, nil)
}
