package cloudreve

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
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/cookie"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
	"github.com/go-resty/resty/v2"
	jsoniter "github.com/json-iterator/go"
)

// do others that not defined in Driver interface

const loginPath = "/user/session"

func (d *Cloudreve) getUA() string {
	if d.CustomUA != "" {
		return d.CustomUA
	}
	return base.UserAgent
}

func (d *Cloudreve) request(method string, path string, callback base.ReqCallback, out interface{}) error {
	if d.ref != nil {
		return d.ref.request(method, path, callback, out)
	}
	u := d.Address + "/api/v3" + path
	req := base.RestyClient.R()
	req.SetHeaders(map[string]string{
		"Cookie":     "cloudreve-session=" + d.Cookie,
		"Accept":     "application/json, text/plain, */*",
		"User-Agent": d.getUA(),
	})

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

		// 刷新 cookie
		if r.Code == http.StatusUnauthorized && path != loginPath {
			if d.Username != "" && d.Password != "" {
				err = d.login()
				if err != nil {
					return err
				}
				return d.request(method, path, callback, out)
			}
		}

		return errors.New(r.Msg)
	}
	sess := cookie.GetCookie(resp.Cookies(), "cloudreve-session")
	if sess != nil {
		d.Cookie = sess.Value
	}
	if out != nil && r.Data != nil {
		var marshal []byte
		marshal, err = jsoniter.Marshal(r.Data)
		if err != nil {
			return err
		}
		err = jsoniter.Unmarshal(marshal, out)
		if err != nil {
			return err
		}
	}

	return nil
}

func (d *Cloudreve) login() error {
	var siteConfig Config
	err := d.request(http.MethodGet, "/site/config", nil, &siteConfig)
	if err != nil {
		return err
	}
	for i := 0; i < 5; i++ {
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

func (d *Cloudreve) doLogin(needCaptcha bool) error {
	var captchaCode string
	var err error
	if needCaptcha {
		var captcha string
		err = d.request(http.MethodGet, "/site/captcha", nil, &captcha)
		if err != nil {
			return err
		}
		if len(captcha) == 0 {
			return errors.New("can not get captcha")
		}
		i := strings.Index(captcha, ",")
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(captcha[i+1:]))
		vRes, err := base.RestyClient.R().SetMultipartField(
			"image", "validateCode.png", "image/png", dec).
			Post(setting.GetStr(conf.OcrApi))
		if err != nil {
			return err
		}
		if jsoniter.Get(vRes.Body(), "status").ToInt() != 200 {
			return errors.New("ocr error:" + jsoniter.Get(vRes.Body(), "msg").ToString())
		}
		captchaCode = jsoniter.Get(vRes.Body(), "result").ToString()
	}
	var resp Resp
	err = d.request(http.MethodPost, loginPath, func(req *resty.Request) {
		req.SetBody(base.Json{
			"username":    d.Addition.Username,
			"Password":    d.Addition.Password,
			"captchaCode": captchaCode,
		})
	}, &resp)
	return err
}

func convertSrc(obj model.Obj) map[string]interface{} {
	m := make(map[string]interface{})
	var dirs []string
	var items []string
	if obj.IsDir() {
		dirs = append(dirs, obj.GetID())
	} else {
		items = append(items, obj.GetID())
	}
	m["dirs"] = dirs
	m["items"] = items
	return m
}

func (d *Cloudreve) GetThumb(file Object) (model.Thumbnail, error) {
	if !d.Addition.EnableThumbAndFolderSize {
		return model.Thumbnail{}, nil
	}
	req := base.NoRedirectClient.R()
	req.SetHeaders(map[string]string{
		"Cookie":     "cloudreve-session=" + d.Cookie,
		"Accept":     "image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8",
		"User-Agent": d.getUA(),
	})
	resp, err := req.Execute(http.MethodGet, d.Address+"/api/v3/file/thumb/"+file.Id)
	if err != nil {
		return model.Thumbnail{}, err
	}
	return model.Thumbnail{
		Thumbnail: resp.Header().Get("Location"),
	}, nil
}

func (d *Cloudreve) upLocal(ctx context.Context, stream model.FileStreamer, u UploadInfo, up driver.UpdateProgress) error {
	var finish int64 = 0
	var chunk int = 0
	DEFAULT := int64(u.ChunkSize)
	for finish < stream.GetSize() {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		left := stream.GetSize() - finish
		byteSize := min(left, DEFAULT)
		utils.Log.Debugf("[Cloudreve-Local] upload range: %d-%d/%d", finish, finish+byteSize-1, stream.GetSize())
		byteData := make([]byte, byteSize)
		n, err := io.ReadFull(stream, byteData)
		utils.Log.Debug(err, n)
		if err != nil {
			return err
		}
		err = d.request(http.MethodPost, "/file/upload/"+u.SessionID+"/"+strconv.Itoa(chunk), func(req *resty.Request) {
			req.SetHeader("Content-Type", "application/octet-stream")
			req.SetContentLength(true)
			req.SetHeader("Content-Length", strconv.FormatInt(byteSize, 10))
			req.SetHeader("User-Agent", d.getUA())
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
		up(float64(finish) * 100 / float64(stream.GetSize()))
		chunk++
	}
	return nil
}

func (d *Cloudreve) upRemote(ctx context.Context, stream model.FileStreamer, u UploadInfo, up driver.UpdateProgress) error {
	DEFAULT := int64(u.ChunkSize)
	ss, err := streamPkg.NewStreamSectionReader(stream, int(DEFAULT), &up)
	if err != nil {
		return err
	}

	uploadUrl := u.UploadURLs[0]
	credential := u.Credential
	var finish int64 = 0
	var chunk int = 0
	for finish < stream.GetSize() {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		left := stream.GetSize() - finish
		byteSize := min(left, DEFAULT)
		utils.Log.Debugf("[Cloudreve-Remote] upload range: %d-%d/%d", finish, finish+byteSize-1, stream.GetSize())
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
		up(float64(finish) * 100 / float64(stream.GetSize()))
		chunk++
	}
	return nil
}

func (d *Cloudreve) upOneDrive(ctx context.Context, stream model.FileStreamer, u UploadInfo, up driver.UpdateProgress) error {
	DEFAULT := int64(u.ChunkSize)
	ss, err := streamPkg.NewStreamSectionReader(stream, int(DEFAULT), &up)
	if err != nil {
		return err
	}

	uploadUrl := u.UploadURLs[0]
	var finish int64 = 0
	for finish < stream.GetSize() {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		left := stream.GetSize() - finish
		byteSize := min(left, DEFAULT)
		utils.Log.Debugf("[Cloudreve-OneDrive] upload range: %d-%d/%d", finish, finish+byteSize-1, stream.GetSize())
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
				req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", finish, finish+byteSize-1, stream.GetSize()))
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
		up(float64(finish) * 100 / float64(stream.GetSize()))
	}
	// 上传成功发送回调请求
	return d.request(http.MethodPost, "/callback/onedrive/finish/"+u.SessionID, func(req *resty.Request) {
		req.SetBody("{}")
	}, nil)
}

func (d *Cloudreve) upS3(ctx context.Context, stream model.FileStreamer, u UploadInfo, up driver.UpdateProgress) error {
	DEFAULT := int64(u.ChunkSize)
	ss, err := streamPkg.NewStreamSectionReader(stream, int(DEFAULT), &up)
	if err != nil {
		return err
	}

	var finish int64 = 0
	var chunk int = 0
	var etags []string
	for finish < stream.GetSize() {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		left := stream.GetSize() - finish
		byteSize := min(left, DEFAULT)
		utils.Log.Debugf("[Cloudreve-S3] upload range: %d-%d/%d", finish, finish+byteSize-1, stream.GetSize())
		rd, err := ss.GetSectionReader(finish, byteSize)
		if err != nil {
			return err
		}
		err = retry.Do(
			func() error {
				rd.Seek(0, io.SeekStart)
				req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.UploadURLs[chunk],
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
		up(float64(finish) * 100 / float64(stream.GetSize()))
		chunk++
	}
	// s3LikeFinishUpload
	// https://github.com/cloudreve/frontend/blob/b485bf297974cbe4834d2e8e744ae7b7e5b2ad39/src/component/Uploader/core/api/index.ts#L204-L252
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
	err = d.request(http.MethodGet, "/callback/s3/"+u.SessionID, nil, nil)
	if err != nil {
		return err
	}
	return nil
}
