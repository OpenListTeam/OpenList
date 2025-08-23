package teldrive

import (
	"fmt"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

// do others that not defined in Driver interface

func (d *Teldrive) request(method string, pathname string, callback base.ReqCallback, resp interface{}) error {
	url := d.Address + pathname
	req := base.RestyClient.R()
	req.SetHeader("Cookie", d.Cookie)
	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}
	var e ErrResp
	req.SetError(&e)
	_req, err := req.Execute(method, url)
	if err != nil {
		return err
	}

	if _req.IsError() {
		return &e
	}
	return nil
}

func (d *Teldrive) getFile(path, name string, isFolder bool) (model.Obj, error) {
	resp := &ListResp{}
	err := d.request(http.MethodGet, "/api/files", func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"path": path,
			"name": name,
			"type": func() string {
				if isFolder {
					return "folder"
				}
				return "file"
			}(),
			"operation": "find",
		})
	}, resp)
	if err != nil {
		return nil, err
	}
	if len(resp.Items) == 0 {
		return nil, fmt.Errorf("file not found: %s/%s", path, name)
	}
	obj := resp.Items[0]
	return &model.Object{
		ID:       obj.ID,
		Name:     obj.Name,
		Size:     obj.Size,
		IsFolder: obj.Type == "folder",
	}, err
}

func (err *ErrResp) Error() string {
	if err == nil {
		return ""
	}

	return fmt.Sprintf("[Teldrive] message:%s Error code:%d", err.Message, err.Code)
}

func (d *Teldrive) createShareFile(fileId string) error {
	var errResp ErrResp
	if err := d.request(http.MethodPost, "/api/files/{id}/share", func(req *resty.Request) {
		req.SetPathParam("id", fileId)
		req.SetBody(base.Json{
			"expiresAt": getDateTime(),
		})
	}, &errResp); err != nil {
		return err
	}

	if errResp.Message != "" {
		return &errResp
	}

	return nil
}

func (d *Teldrive) getShareFileById(fileId string) (*ShareObj, error) {
	var shareObj ShareObj
	if err := d.request(http.MethodGet, "/api/files/{id}/share", func(req *resty.Request) {
		req.SetPathParam("id", fileId)
	}, &shareObj); err != nil {
		return nil, err
	}

	return &shareObj, nil
}

func getDateTime() string {
	now := time.Now().UTC()
	formattedWithMs := now.Add(time.Hour * 1).Format("2006-01-02T15:04:05.000Z")
	return formattedWithMs
}
