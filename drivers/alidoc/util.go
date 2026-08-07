package alidoc

import (
	"context"
	"fmt"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

const apiBase = "https://alidocs.dingtalk.com"

func (d *AliDoc) request(ctx context.Context) *resty.Request {
	return d.client.R().
		SetContext(ctx).
		SetHeader("Cookie", d.Cookie).
		SetHeader("Accept", "application/json, text/plain, */*").
		SetHeader("Referer", apiBase+"/").
		SetHeader("Origin", apiBase)
}

func msToTime(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

func checkResp(resp *resty.Response, result apiResp) error {
	if resp != nil && resp.IsError() {
		if msg := result.ErrMessage(); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return fmt.Errorf("http error: %d", resp.StatusCode())
	}
	if !result.IsSuccess || result.Status != 200 {
		msg := result.ErrMessage()
		if msg == "" {
			msg = "request failed"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func toObj(item dentry) model.Obj {
	return &model.Object{
		ID:       item.DentryUUID,
		Name:     item.Name,
		Size:     item.FileSize,
		Modified: msToTime(item.UpdatedTime),
		Ctime:    msToTime(item.CreatedTime),
		IsFolder: item.DentryType == "folder",
	}
}

func firstDownloadURL(resp downloadResp) (string, error) {
	if len(resp.Data.OSSURLPreSignatureInfo.PreSignURLs) == 0 {
		return "", fmt.Errorf("empty download url")
	}
	return resp.Data.OSSURLPreSignatureInfo.PreSignURLs[0], nil
}

func newClient() *resty.Client {
	client := base.NewRestyClient()
	client.SetHeader("User-Agent", base.UserAgent)
	return client
}

func (d *AliDoc) post(ctx context.Context, path string, body interface{}) error {
	var result apiResp
	resp, err := d.request(ctx).
		SetBody(body).
		SetResult(&result).
		SetError(&result).
		Post(apiBase + path)
	if err != nil {
		return err
	}
	return checkResp(resp, result)
}

func (d *AliDoc) checkCookie(ctx context.Context) error {
	var result apiResp
	resp, err := d.request(ctx).
		SetResult(&result).
		SetError(&result).
		Get(apiBase + "/portal/api/v1/mine/info")
	if err != nil {
		return err
	}
	return checkResp(resp, result)
}

func (d *AliDoc) list(ctx context.Context, dentryUUID string) ([]dentry, error) {
	var result listResp
	resp, err := d.request(ctx).
		SetQueryParam("dentryUuid", dentryUUID).
		SetQueryParam("withParentAncestors", "true").
		SetQueryParam("orderType", "SORT_KEY").
		SetQueryParam("sortType", "desc").
		SetQueryParam("listDentrySource", "2").
		SetQueryParam("pageSize", "1000").
		SetResult(&result).
		SetError(&result).
		Get(apiBase + "/box/api/v2/dentry/list")
	if err != nil {
		return nil, err
	}
	if err := checkResp(resp, result.apiResp); err != nil {
		return nil, err
	}
	return result.Data.Children, nil
}

func (d *AliDoc) download(ctx context.Context, dentryUUID string) (downloadResp, error) {
	var result downloadResp
	resp, err := d.request(ctx).
		SetQueryParam("dentryUuid", dentryUUID).
		SetQueryParam("version", "1").
		SetQueryParam("supportDownloadTypes", "URL_PRE_SIGNATURE,HTTP_TO_CENTER").
		SetQueryParam("downloadType", "URL_PRE_SIGNATURE").
		SetResult(&result).
		SetError(&result).
		Get(apiBase + "/box/api/v2/file/download")
	if err != nil {
		return result, err
	}
	if err := checkResp(resp, result.apiResp); err != nil {
		return result, err
	}
	return result, nil
}
