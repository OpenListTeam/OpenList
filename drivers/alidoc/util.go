package alidoc

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
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

func joinPath(basePath, name string) string {
	if basePath == "" || basePath == "/" {
		return "/" + name
	}
	return strings.TrimRight(basePath, "/") + "/" + name
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

func toObj(parentPath string, item dentry) model.Obj {
	return toObjWithPath(joinPath(parentPath, item.Name), item)
}

func toObjUsingBestPath(parentPath string, item dentry) model.Obj {
	fullPath := strings.TrimSpace(item.Path)
	if fullPath == "" {
		fullPath = joinPath(parentPath, item.Name)
	}
	return toObjWithPath(fullPath, item)
}

func toObjWithPath(fullPath string, item dentry) model.Obj {
	name := item.Name
	if name == "" {
		name = path.Base(fullPath)
	}
	obj := &Object{
		Object: model.Object{
			ID:       item.DentryUUID,
			Path:     fullPath,
			Name:     name,
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

func readonlyError() error {
	return fmt.Errorf("alidoc driver is read-only: %w", errs.NotSupport)
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
