package onedrive_app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
	"github.com/go-resty/resty/v2"
	jsoniter "github.com/json-iterator/go"
)

var onedriveHostMap = map[string]Host{
	"global": {
		Oauth: "https://login.microsoftonline.com",
		Api:   "https://graph.microsoft.com",
	},
	"cn": {
		Oauth: "https://login.chinacloudapi.cn",
		Api:   "https://microsoftgraph.chinacloudapi.cn",
	},
	"us": {
		Oauth: "https://login.microsoftonline.us",
		Api:   "https://graph.microsoft.us",
	},
	"de": {
		Oauth: "https://login.microsoftonline.de",
		Api:   "https://graph.microsoft.de",
	},
}

// NormalizeSiteID converts any accepted SharePoint site identifier into the form
// expected by the /sites/{site-id} segment.
//
// Accepted inputs:
//
//	contoso.sharepoint.com,<siteGuid>,<webGuid>
//	contoso.sharepoint.com:/sites/<name> [/sites/<name>:]
//	https://contoso.sharepoint.com/sites/<name>
//	https://contoso.sharepoint.com            (site collection root)
//
// The path form must keep its trailing colon, otherwise Graph rejects the URL.
// A path-form identifier still has to be resolved to the comma form before it can
// be followed by /drive; see resolveSiteID.
func NormalizeSiteID(siteID string) string {
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		return ""
	}
	if strings.HasPrefix(siteID, "https://") || strings.HasPrefix(siteID, "http://") {
		if u, err := url.Parse(siteID); err == nil && u.Host != "" {
			siteID = u.Host + ":" + strings.TrimSuffix(u.Path, "/")
		}
	}
	if strings.Contains(siteID, ":/") {
		siteID = strings.TrimSuffix(siteID, "/")
		if !strings.HasSuffix(siteID, ":") {
			siteID += ":"
		}
	}
	return siteID
}

// resolveSiteID turns a site path identifier into the opaque comma-separated site
// id, since Graph only accepts /sites/{path}:/ as a terminal segment — it cannot be
// followed by /drive. Already-resolved ids are left untouched.
func (d *OnedriveAPP) resolveSiteID() error {
	siteID := NormalizeSiteID(d.SiteID)
	if strings.Contains(siteID, ",") {
		d.SiteID = siteID
		return nil
	}
	host := onedriveHostMap[d.Region]
	var resp struct {
		Id string `json:"id"`
	}
	_, err := d.Request(fmt.Sprintf("%s/v1.0/sites/%s?$select=id", host.Api, siteID), http.MethodGet, nil, &resp)
	if err != nil {
		return err
	}
	if resp.Id == "" {
		return fmt.Errorf("failed to resolve sharepoint site %q", d.SiteID)
	}
	d.SiteID = resp.Id
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *OnedriveAPP) GetMetaUrl(auth bool, path string) string {
	host := onedriveHostMap[d.Region]
	path = utils.EncodePath(path, true)
	if auth {
		return host.Oauth
	}
	var base string
	if d.IsSharepoint {
		base = fmt.Sprintf("%s/v1.0/sites/%s/drive/root", host.Api, NormalizeSiteID(d.SiteID))
	} else {
		base = fmt.Sprintf("%s/v1.0/users/%s/drive/root", host.Api, d.Email)
	}
	if path == "/" || path == "\\" {
		return base
	}
	return base + ":" + path + ":"
}

func (d *OnedriveAPP) accessToken() error {
	var err error
	for i := 0; i < 3; i++ {
		err = d._accessToken()
		if err == nil {
			break
		}
	}
	return err
}

func (d *OnedriveAPP) _accessToken() error {
	url := d.GetMetaUrl(true, "") + "/" + d.TenantID + "/oauth2/token"
	var resp base.TokenResp
	var e TokenErr
	_, err := base.RestyClient.R().SetResult(&resp).SetError(&e).SetFormData(map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     d.ClientID,
		"client_secret": d.ClientSecret,
		"resource":      onedriveHostMap[d.Region].Api + "/",
		"scope":         onedriveHostMap[d.Region].Api + "/.default",
	}).Post(url)
	if err != nil {
		return err
	}
	if e.Error != "" {
		return fmt.Errorf("%s", e.ErrorDescription)
	}
	if resp.AccessToken == "" {
		return errs.EmptyToken
	}
	d.AccessToken = resp.AccessToken
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *OnedriveAPP) Request(url string, method string, callback base.ReqCallback, resp interface{}, noRetry ...bool) ([]byte, error) {
	req := base.RestyClient.R()
	req.SetHeader("Authorization", "Bearer "+d.AccessToken)
	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}
	var e RespErr
	req.SetError(&e)
	res, err := req.Execute(method, url)
	if err != nil {
		return nil, err
	}
	if e.Error.Code != "" {
		if e.Error.Code == "InvalidAuthenticationToken" && !utils.IsBool(noRetry...) {
			err = d.accessToken()
			if err != nil {
				return nil, err
			}
			return d.Request(url, method, callback, resp)
		}
		return nil, errors.New(e.Error.Message)
	}
	return res.Body(), nil
}

func (d *OnedriveAPP) getFiles(path string) ([]File, error) {
	var res []File
	nextLink := d.GetMetaUrl(false, path) + "/children?$top=1000&$expand=thumbnails($select=medium)&$select=id,name,size,lastModifiedDateTime,content.downloadUrl,file,parentReference"
	for nextLink != "" {
		var files Files
		_, err := d.Request(nextLink, http.MethodGet, nil, &files)
		if err != nil {
			return nil, err
		}
		res = append(res, files.Value...)
		nextLink = files.NextLink
	}
	return res, nil
}

func (d *OnedriveAPP) GetFile(path string) (*File, error) {
	var file File
	u := d.GetMetaUrl(false, path)
	_, err := d.Request(u, http.MethodGet, nil, &file)
	return &file, err
}

func (d *OnedriveAPP) upSmall(ctx context.Context, dstDir model.Obj, stream model.FileStreamer) error {
	url := d.GetMetaUrl(false, stdpath.Join(dstDir.GetPath(), stream.GetName())) + "/content"
	_, err := d.Request(url, http.MethodPut, func(req *resty.Request) {
		req.SetBody(driver.NewLimitedUploadStream(ctx, stream)).SetContext(ctx)
	}, nil)
	return err
}

func (d *OnedriveAPP) upBig(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	url := d.GetMetaUrl(false, stdpath.Join(dstDir.GetPath(), stream.GetName())) + "/createUploadSession"
	res, err := d.Request(url, http.MethodPost, nil, nil)
	if err != nil {
		return err
	}
	DEFAULT := d.ChunkSize * 1024 * 1024
	ss, err := streamPkg.NewStreamSectionReader(stream, int(DEFAULT), &up)
	if err != nil {
		return err
	}

	uploadUrl := jsoniter.Get(res, "uploadUrl").ToString()
	var finish int64 = 0
	for finish < stream.GetSize() {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		left := stream.GetSize() - finish
		byteSize := min(left, DEFAULT)
		utils.Log.Debugf("[OnedriveAPP] upload range: %d-%d/%d", finish, finish+byteSize-1, stream.GetSize())
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
	return nil
}

func (d *OnedriveAPP) getDrive(ctx context.Context) (*DriveResp, error) {
	host, _ := onedriveHostMap[d.Region]
	var api string
	if d.IsSharepoint {
		api = fmt.Sprintf("%s/v1.0/sites/%s/drive", host.Api, NormalizeSiteID(d.SiteID))
	} else {
		api = fmt.Sprintf("%s/v1.0/users/%s/drive", host.Api, d.Email)
	}
	var resp DriveResp
	_, err := d.Request(api, http.MethodGet, func(req *resty.Request) {
		req.SetContext(ctx)
	}, &resp, true)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (d *OnedriveAPP) getDirectUploadInfo(ctx context.Context, path string) (*model.HttpDirectUploadInfo, error) {
	// Create upload session
	url := d.GetMetaUrl(false, path) + "/createUploadSession"
	metadata := map[string]any{
		"item": map[string]any{
			"@microsoft.graph.conflictBehavior": "rename",
		},
	}

	res, err := d.Request(url, http.MethodPost, func(req *resty.Request) {
		req.SetBody(metadata).SetContext(ctx)
	}, nil)
	if err != nil {
		return nil, err
	}

	uploadUrl := jsoniter.Get(res, "uploadUrl").ToString()
	if uploadUrl == "" {
		return nil, fmt.Errorf("failed to get upload URL from response")
	}
	return &model.HttpDirectUploadInfo{
		UploadURL: uploadUrl,
		ChunkSize: d.ChunkSize * 1024 * 1024, // Convert MB to bytes
		Method:    "PUT",
	}, nil
}
