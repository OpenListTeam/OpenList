package google_drive

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

type GoogleDrive struct {
	model.Storage
	Addition
	AccessToken            string
	ServiceAccountFile     int
	ServiceAccountFileList []string

	// 驱动级客户端（用于支持仅本驱动走代理）
	RestyClient      *resty.Client
	NoRedirectClient *resty.Client
	HttpClient       *http.Client
}

func (d *GoogleDrive) Config() driver.Config {
	return config
}

func (d *GoogleDrive) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *GoogleDrive) Init(ctx context.Context) error {
	if d.ChunkSize == 0 {
		d.ChunkSize = 5
	}

	// 初始化驱动级 Resty 客户端（尽量 clone 全局，避免改变全局行为）
	if base.RestyClient != nil {
		d.RestyClient = base.RestyClient.Clone()
	} else {
		d.RestyClient = resty.New()
	}
	if base.NoRedirectClient != nil {
		d.NoRedirectClient = base.NoRedirectClient.Clone()
	} else {
		d.NoRedirectClient = resty.New()
	}

	// 初始化驱动级 http.Client（尽量保留 base.HttpClient 的 transport/timeout）
	if base.HttpClient != nil {
		var tr *http.Transport
		if t, ok := base.HttpClient.Transport.(*http.Transport); ok && t != nil {
			tr = t.Clone()
		} else {
			tr = &http.Transport{}
		}
		d.HttpClient = &http.Client{
			Transport: tr,
			Timeout:   base.HttpClient.Timeout,
		}
	} else {
		d.HttpClient = &http.Client{}
	}

	// 若开启代理并提供了代理地址，则尝试设置代理（解析失败则忽略代理设置）
	if d.ProxyEnable && d.ProxyAddress != "" {
		if parsed, err := url.Parse(d.ProxyAddress); err == nil {
			// resty 设置代理
			d.RestyClient.SetProxy(d.ProxyAddress)
			d.NoRedirectClient.SetProxy(d.ProxyAddress)

			// http.Transport 设置代理
			if tr, ok := d.HttpClient.Transport.(*http.Transport); ok && tr != nil {
				tr.Proxy = http.ProxyURL(parsed)
				d.HttpClient.Transport = tr
			} else {
				d.HttpClient.Transport = &http.Transport{
					Proxy: http.ProxyURL(parsed),
				}
			}
		}
	}

	return d.refreshToken()
}

func (d *GoogleDrive) Drop(ctx context.Context) error {
	return nil
}

func (d *GoogleDrive) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.getFiles(dir.GetID())
	if err != nil {
		return nil, err
	}
	return utils.SliceConvert(files, func(src File) (model.Obj, error) {
		return fileToObj(src), nil
	})
}

func (d *GoogleDrive) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	url := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s?includeItemsFromAllDrives=true&supportsAllDrives=true", file.GetID())
	_, err := d.request(url, http.MethodGet, nil, nil)
	if err != nil {
		return nil, err
	}
	link := model.Link{
		URL: url + "&alt=media&acknowledgeAbuse=true",
		Header: http.Header{
			"Authorization": []string{"Bearer " + d.AccessToken},
		},
	}
	return &link, nil
}

func (d *GoogleDrive) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	data := base.Json{
		"name":     dirName,
		"parents":  []string{parentDir.GetID()},
		"mimeType": "application/vnd.google-apps.folder",
	}
	_, err := d.request("https://www.googleapis.com/drive/v3/files", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	return err
}

func (d *GoogleDrive) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	query := map[string]string{
		"addParents":    dstDir.GetID(),
		"removeParents": "root",
	}
	url := "https://www.googleapis.com/drive/v3/files/" + srcObj.GetID()
	_, err := d.request(url, http.MethodPatch, func(req *resty.Request) {
		req.SetQueryParams(query)
	}, nil)
	return err
}

func (d *GoogleDrive) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	data := base.Json{
		"name": newName,
	}
	url := "https://www.googleapis.com/drive/v3/files/" + srcObj.GetID()
	_, err := d.request(url, http.MethodPatch, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	return err
}

func (d *GoogleDrive) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return errs.NotSupport
}

func (d *GoogleDrive) Remove(ctx context.Context, obj model.Obj) error {
	url := "https://www.googleapis.com/drive/v3/files/" + obj.GetID()
	_, err := d.request(url, http.MethodDelete, nil, nil)
	return err
}

func (d *GoogleDrive) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	obj := stream.GetExist()
	var (
		e    Error
		url  string
		data base.Json
		res  *resty.Response
		err  error
	)
	if obj != nil {
		url = fmt.Sprintf("https://www.googleapis.com/upload/drive/v3/files/%s?uploadType=resumable&supportsAllDrives=true", obj.GetID())
		data = base.Json{}
	} else {
		data = base.Json{
			"name":    stream.GetName(),
			"parents": []string{dstDir.GetID()},
		}
		url = "https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&supportsAllDrives=true"
	}
	req := d.NoRedirectClient.R().
		SetHeaders(map[string]string{
			"Authorization":           "Bearer " + d.AccessToken,
			"X-Upload-Content-Type":   stream.GetMimetype(),
			"X-Upload-Content-Length": strconv.FormatInt(stream.GetSize(), 10),
		}).
		SetError(&e).SetBody(data).SetContext(ctx)
	if obj != nil {
		res, err = req.Patch(url)
	} else {
		res, err = req.Post(url)
	}
	if err != nil {
		return err
	}
	if e.Error.Code != 0 {
		if e.Error.Code == 401 {
			err = d.refreshToken()
			if err != nil {
				return err
			}
			return d.Put(ctx, dstDir, stream, up)
		}
		return fmt.Errorf("%s: %v", e.Error.Message, e.Error.Errors)
	}
	putUrl := res.Header().Get("location")
	if stream.GetSize() < d.ChunkSize*1024*1024 {
		_, err = d.request(putUrl, http.MethodPut, func(req *resty.Request) {
			req.SetHeader("Content-Length", strconv.FormatInt(stream.GetSize(), 10)).
				SetBody(driver.NewLimitedUploadStream(ctx, stream))
		}, nil)
	} else {
		err = d.chunkUpload(ctx, stream, putUrl, up)
	}
	return err
}

var _ driver.Driver = (*GoogleDrive)(nil)
