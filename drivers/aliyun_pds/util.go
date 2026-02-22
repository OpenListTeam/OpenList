package aliyun_pds

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

// do others that not defined in Driver interface

func (d *AliPDS) refreshToken() error {
	url := d.AuthEndpoint + "/v2/account/token"
	var resp base.TokenResp
	var e RespErr
	_, err := base.RestyClient.R().
		//ForceContentType("application/json").
		SetBody(base.Json{"refresh_token": d.RefreshToken, "grant_type": "refresh_token"}).
		SetResult(&resp).
		SetError(&e).
		Post(url)
	if err != nil {
		return err
	}
	if e.Code != "" {
		return fmt.Errorf("failed to refresh token: %s", e.Message)
	}
	if resp.RefreshToken == "" {
		return errors.New("failed to refresh token: refresh token is empty")
	}
	d.RefreshToken, d.AccessToken = resp.RefreshToken, resp.AccessToken
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *AliPDS) request(url, method string, callback base.ReqCallback, resp interface{}) ([]byte, error, RespErr) {
	req := base.RestyClient.R()
	req.SetHeaders(map[string]string{
		"Authorization": "Bearer\t" + d.AccessToken,
		"content-type":  "application/json",
		"origin":        d.UIEndpoint,
		"Referer":       d.UIEndpoint + "/",
	})
	if callback != nil {
		callback(req)
	} else {
		req.SetBody("{}")
	}
	if resp != nil {
		req.SetResult(resp)
	}
	var e RespErr
	req.SetError(&e)
	res, err := req.Execute(method, url)
	if err != nil {
		return nil, err, e
	}
	if e.Code != "" {
		switch e.Code {
		case "AccessTokenInvalid":
			err = d.refreshToken()
			if err != nil {
				return nil, err, e
			}
		default:
			return nil, errors.New(e.Message), e
		}
		return d.request(url, method, callback, resp)
	} else if res.IsError() {
		return nil, errors.New("bad status code " + res.Status()), e
	}
	return res.Body(), nil, e
}

func (d *AliPDS) getFiles(fileId string) ([]File, error) {
	marker := "first"
	res := make([]File, 0)
	for marker != "" {
		if marker == "first" {
			marker = ""
		}
		var resp Files
		data := base.Json{
			"domain_id":               d.DomainID,
			"drive_id":                d.DriveID,
			"fields":                  "*",
			"image_thumbnail_process": "image/resize,w_400/format,jpeg",
			"image_url_process":       "image/resize,w_1920/format,jpeg",
			"limit":                   200,
			"marker":                  marker,
			"order_by":                d.OrderBy,
			"order_direction":         d.OrderDirection,
			"parent_file_id":          fileId,
			"video_thumbnail_process": "video/snapshot,t_0,f_jpg,ar_auto,w_300",
			"url_expire_sec":          7200,
		}
		_, err, _ := d.request(d.ApiEndpoint+"/v2/file/list", http.MethodPost, func(req *resty.Request) {
			req.SetBody(data)
		}, &resp)

		if err != nil {
			return nil, err
		}
		marker = resp.NextMarker
		res = append(res, resp.Items...)
	}
	return res, nil
}

func (d *AliPDS) batch(srcId, dstId string, url string) error {
	res, err, _ := d.request(d.ApiEndpoint+"/v2/batch", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"requests": []base.Json{
				{
					"headers": base.Json{
						"Content-Type": "application/json",
					},
					"method": "POST",
					"id":     srcId,
					"body": base.Json{
						"drive_id":          d.DriveID,
						"file_id":           srcId,
						"to_drive_id":       d.DriveID,
						"to_parent_file_id": dstId,
					},
					"url": url,
				},
			},
			"resource": "file",
		})
	}, nil)
	if err != nil {
		return err
	}
	status := utils.Json.Get(res, "responses", 0, "status").ToInt()
	if status < 400 && status >= 100 {
		return nil
	}
	return errors.New(string(res))
}
