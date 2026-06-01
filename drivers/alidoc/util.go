package alidoc

import (
	"context"
	"fmt"
	"strings"
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
	obj := &Object{
		Object: model.Object{
			ID:       item.DentryUUID,
			Name:     item.Name,
			Size:     item.FileSize,
			Modified: msToTime(item.UpdatedTime),
			Ctime:    msToTime(item.CreatedTime),
			IsFolder: item.DentryType == "folder",
		},
		DentryType:  item.DentryType,
		ContentType: item.ContentType,
		Extension:   item.Extension,
		PreviewURL:  item.URL.PCChildAppPreviewURL,
		EditURL:     item.URL.PCChildAppURL,
	}
	if obj.IsDir() && item.DentryStatistic.ChildrenCount > 0 && obj.Size == 0 {
		// Keep size as 0 for folders; childrenCount is intentionally ignored.
	}
	return obj
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

func aliDocObjID(obj model.Obj) string {
	if obj == nil {
		return ""
	}
	return strings.TrimSpace(obj.GetID())
}

func (d *AliDoc) parentID(dir model.Obj) string {
	if id := aliDocObjID(dir); id != "" {
		return id
	}
	return d.RootFolderID
}

func pickAliDocDentryType(obj model.Obj) string {
	if o, ok := obj.(*Object); ok && strings.TrimSpace(o.DentryType) != "" {
		return o.DentryType
	}
	if obj != nil && obj.IsDir() {
		return "folder"
	}
	return "file"
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
