package onedrive

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

func (d *Onedrive) GetMetaUrl(auth bool, path string) string {
	host, _ := onedriveHostMap[d.Region]
	path = utils.EncodePath(path, true)
	if auth {
		return host.Oauth
	}
	if d.IsSharepoint {
		if path == "/" || path == "\\" {
			return fmt.Sprintf("%s/v1.0/sites/%s/drive/root", host.Api, d.SiteId)
		} else {
			return fmt.Sprintf("%s/v1.0/sites/%s/drive/root:%s:", host.Api, d.SiteId, path)
		}
	} else {
		if path == "/" || path == "\\" {
			return fmt.Sprintf("%s/v1.0/me/drive/root", host.Api)
		} else {
			return fmt.Sprintf("%s/v1.0/me/drive/root:%s:", host.Api, path)
		}
	}
}

func (d *Onedrive) refreshToken() error {
	var err error
	for i := 0; i < 3; i++ {
		err = d._refreshToken()
		if err == nil {
			break
		}
	}
	return err
}

func (d *Onedrive) _refreshToken() error {
	// 使用在线API刷新Token，无需ClientID和ClientSecret
	if d.UseOnlineAPI && len(d.APIAddress) > 0 {
		u := d.APIAddress
		var resp struct {
			RefreshToken string `json:"refresh_token"`
			AccessToken  string `json:"access_token"`
			ErrorMessage string `json:"text"`
		}
		_, err := base.RestyClient.R().
			SetResult(&resp).
			SetQueryParams(map[string]string{
				"refresh_ui": d.RefreshToken,
				"server_use": "true",
				"driver_txt": "onedrive_pr",
			}).
			Get(u)
		if err != nil {
			return err
		}
		if resp.RefreshToken == "" || resp.AccessToken == "" {
			if resp.ErrorMessage != "" {
				return fmt.Errorf("failed to refresh token: %s", resp.ErrorMessage)
			}
			return fmt.Errorf("empty token returned from official API, a wrong refresh token may have been used")
		}
		d.AccessToken = resp.AccessToken
		d.RefreshToken = resp.RefreshToken
		op.MustSaveDriverStorage(d)
		return nil
	}
	// 使用本地客户端的情况下检查是否为空
	if d.ClientID == "" || d.ClientSecret == "" {
		return fmt.Errorf("empty ClientID or ClientSecret")
	}
	// 走原有的刷新逻辑
	url := d.GetMetaUrl(true, "") + "/common/oauth2/v2.0/token"
	var resp base.TokenResp
	var e TokenErr
	_, err := base.RestyClient.R().SetResult(&resp).SetError(&e).SetFormData(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     d.ClientID,
		"client_secret": d.ClientSecret,
		"redirect_uri":  d.RedirectUri,
		"refresh_token": d.RefreshToken,
	}).Post(url)
	if err != nil {
		return err
	}
	if e.Error != "" {
		return fmt.Errorf("%s", e.ErrorDescription)
	}
	if resp.RefreshToken == "" {
		return errs.EmptyToken
	}
	d.RefreshToken, d.AccessToken = resp.RefreshToken, resp.AccessToken
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *Onedrive) Request(url string, method string, callback base.ReqCallback, resp interface{}, noRetry ...bool) ([]byte, error) {
	if d.ref != nil {
		return d.ref.Request(url, method, callback, resp)
	}
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
			err = d.refreshToken()
			if err != nil {
				return nil, err
			}
			return d.Request(url, method, callback, resp)
		}
		return nil, errors.New(e.Error.Message)
	}
	return res.Body(), nil
}

func (d *Onedrive) getFiles(path string) ([]File, error) {
	var res []File
	nextLink := d.GetMetaUrl(false, path) + "/children?$top=1000&$expand=thumbnails($select=medium)&$select=id,name,size,fileSystemInfo,content.downloadUrl,file,parentReference"
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

func (d *Onedrive) GetFile(path string) (*File, error) {
	var file File
	u := d.GetMetaUrl(false, path)
	_, err := d.Request(u, http.MethodGet, nil, &file)
	return &file, err
}

func (d *Onedrive) createLink(path string) (string, error) {
	api := d.GetMetaUrl(false, path) + "/createLink"
	data := base.Json{
		"type":  "view",
		"scope": "anonymous",
	}
	var resp struct {
		Link struct {
			WebUrl string `json:"webUrl"`
		} `json:"link"`
	}
	_, err := d.Request(api, http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, &resp)
	if err != nil {
		return "", err
	}
	if resp.Link.WebUrl == "" {
		return "", fmt.Errorf("createLink returned empty webUrl")
	}

	p, err := url.Parse(resp.Link.WebUrl)
	if err != nil {
		return "", err
	}
	// Do some transformations
	q := url.Values{}
	parts := splitSegs(p.Path)
	if p.Host == "1drv.ms" {
		// For personal
		// https://1drv.ms/t/c/{user}/{share} ->
		// https://my.microsoftpersonalcontent.com/personal/{user}/_layouts/15/download.aspx?share={share}
		if len(parts) < 2 {
			return "", fmt.Errorf("invalid onedrive short link: %s", resp.Link.WebUrl)
		}
		// take last two segments as user and share when available
		user := parts[len(parts)-2]
		share := parts[len(parts)-1]
		p.Host = "my.microsoftpersonalcontent.com"
		p.Path = fmt.Sprintf("/personal/%s/_layouts/15/download.aspx", user)
		q.Set("share", share)
	} else {
		// https://{tenant}-my.sharepoint.com/:u:/g/personal/{user_email}/{share}
		// https://{tenant}-my.sharepoint.com/personal/{user_email}/_layouts/15/download.aspx?share={share}
		// Find the "personal" segment and extract user and share after it.
		idx := -1
		for i, s := range parts {
			if s == "personal" {
				idx = i
				break
			}
		}
		if idx == -1 || idx+2 >= len(parts) {
			return "", fmt.Errorf("invalid onedrive sharepoint link: %s", resp.Link.WebUrl)
		}
		user := parts[idx+1]
		share := parts[idx+2]
		p.Path = fmt.Sprintf("/personal/%s/_layouts/15/download.aspx", user)
		q.Set("share", share)
	}
	p.RawQuery = q.Encode()
	return p.String(), nil
}

// splitSegs splits path into non-empty segments.
func splitSegs(path string) []string {
	raw := strings.Split(path, "/")
	segs := make([]string, 0, len(raw))
	for _, s := range raw {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

func (d *Onedrive) upSmall(ctx context.Context, dstDir model.Obj, stream model.FileStreamer) error {
	filepath := stdpath.Join(dstDir.GetPath(), stream.GetName())
	// 1. upload new file
	// ApiDoc: https://learn.microsoft.com/en-us/onedrive/developer/rest-api/api/driveitem_put_content?view=odsp-graph-online
	url := d.GetMetaUrl(false, filepath) + "/content"
	_, err := d.Request(url, http.MethodPut, func(req *resty.Request) {
		req.SetBody(driver.NewLimitedUploadStream(ctx, stream)).SetContext(ctx)
	}, nil)
	if err != nil {
		return fmt.Errorf("onedrive: Failed to upload new file(path=%v): %w", filepath, err)
	}

	// 2. update metadata
	err = d.updateMetadata(ctx, stream, filepath)
	if err != nil {
		return fmt.Errorf("onedrive: Failed to update file(path=%v) metadata: %w", filepath, err)
	}
	return nil
}

func (d *Onedrive) updateMetadata(ctx context.Context, stream model.FileStreamer, filepath string) error {
	url := d.GetMetaUrl(false, filepath)
	metadata := toAPIMetadata(stream)
	// ApiDoc: https://learn.microsoft.com/en-us/onedrive/developer/rest-api/api/driveitem_update?view=odsp-graph-online
	_, err := d.Request(url, http.MethodPatch, func(req *resty.Request) {
		req.SetBody(metadata).SetContext(ctx)
	}, nil)
	return err
}

func toAPIMetadata(stream model.FileStreamer) Metadata {
	metadata := Metadata{
		FileSystemInfo: &FileSystemInfoFacet{},
	}
	if !stream.ModTime().IsZero() {
		metadata.FileSystemInfo.LastModifiedDateTime = stream.ModTime()
	}
	if !stream.CreateTime().IsZero() {
		metadata.FileSystemInfo.CreatedDateTime = stream.CreateTime()
	}
	if stream.CreateTime().IsZero() && !stream.ModTime().IsZero() {
		metadata.FileSystemInfo.CreatedDateTime = stream.CreateTime()
	}
	return metadata
}

func (d *Onedrive) upBig(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	url := d.GetMetaUrl(false, stdpath.Join(dstDir.GetPath(), stream.GetName())) + "/createUploadSession"
	metadata := map[string]any{"item": toAPIMetadata(stream)}
	res, err := d.Request(url, http.MethodPost, func(req *resty.Request) {
		req.SetBody(metadata).SetContext(ctx)
	}, nil)
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
		utils.Log.Debugf("[Onedrive] upload range: %d-%d/%d", finish, finish+byteSize-1, stream.GetSize())
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

func (d *Onedrive) getDrive(ctx context.Context) (*DriveResp, error) {
	var api string
	host, _ := onedriveHostMap[d.Region]
	if d.IsSharepoint {
		api = fmt.Sprintf("%s/v1.0/sites/%s/drive", host.Api, d.SiteId)
	} else {
		api = fmt.Sprintf("%s/v1.0/me/drive", host.Api)
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

func (d *Onedrive) getDirectUploadInfo(ctx context.Context, path string) (*model.HttpDirectUploadInfo, error) {
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
