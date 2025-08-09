package onedrive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	stdpath "path"
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
			SetHeader("User-Agent", "Mozilla/5.0 (Macintosh; Apple macOS 15_5) AppleWebKit/537.36 (KHTML, like Gecko) Safari/537.36 Chrome/138.0.0.0 Openlist/425.6.30").
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

func (d *Onedrive) Request(url string, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
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
		if e.Error.Code == "InvalidAuthenticationToken" {
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
