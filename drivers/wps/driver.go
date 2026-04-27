package wps

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

type Wps struct {
	model.Storage
	Addition

	login  *loginState
	client *resty.Client
}

func (d *Wps) Config() driver.Config {
	return config
}

func (d *Wps) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Wps) Init(ctx context.Context) error {
	if d.Cookie == "" {
		return fmt.Errorf("cookie is empty")
	}

	d.client = base.NewRestyClient()

	resp, err := d.request(ctx).SetResult(&d.login).Get("https://account.kdocs.cn/api/v3/islogin")
	if err != nil {
		return err
	}
	if !resp.IsSuccess() {
		return fmt.Errorf("failed to check login status, status code: %d, body: %s", resp.StatusCode(), resp.String())
	}

	return nil
}

func (d *Wps) Drop(ctx context.Context) error {

	if d.client != nil {
		d.client = nil
	}
	if d.login != nil {
		d.login = nil
	}
	return nil
}

func (d *Wps) List(ctx context.Context, dir model.Obj, _ model.ListArgs) ([]model.Obj, error) {
	basePath := "/"
	if dir != nil {
		if p := dir.GetPath(); p != "" {
			basePath = p
		}
	}
	if basePath == "/" {
		groups, err := d.getGroups(ctx)
		if err != nil {
			return nil, err
		}
		res := make([]model.Obj, 0, len(groups))
		for _, g := range groups {
			path := joinPath(basePath, g.Name)
			obj := &Obj{
				Obj: &model.Object{
					ID:       strconv.FormatInt(g.GroupID, 10),
					Path:     path,
					Name:     g.Name,
					Modified: parseTime(0),
					Ctime:    parseTime(0),
					IsFolder: true,
				},
				Kind:    "group",
				GroupID: g.GroupID,
			}
			res = append(res, obj)
		}
		return res, nil
	}
	node, err := unwrapWpsObj(dir)
	if err != nil {
		return nil, err
	}
	if node.Kind != "group" && node.Kind != "folder" {
		return nil, nil
	}
	parentID := int64(0)
	if node.HasFile && node.Kind == "folder" {
		parentID = node.FileID
	}
	files, err := d.getFiles(ctx, node.GroupID, parentID)
	if err != nil {
		return nil, err
	}
	res := make([]model.Obj, 0, len(files))
	for _, f := range files {
		res = append(res, f.fileToObj(basePath, d.isPersonal()))
	}
	return res, nil
}

func (d *Wps) Link(ctx context.Context, file model.Obj, _ model.LinkArgs) (*model.Link, error) {
	if file == nil {
		return nil, errs.NotSupport
	}
	node, err := unwrapWpsObj(file)
	if err != nil {
		return nil, err
	}
	if node.Kind != "file" || !node.HasFile {
		return nil, errs.NotSupport
	}
	if !node.CanDownload {
		return nil, fmt.Errorf("can not download")
	}
	url := fmt.Sprintf("%s/api/v5/groups/%d/files/%d/download?support_checksums=sha1", d.driveHost()+d.drivePrefix(), node.GroupID, node.FileID)
	var resp downloadResp
	r, err := d.request(ctx).SetResult(&resp).SetError(&resp).Get(url)
	if err != nil {
		return nil, err
	}
	if r != nil && r.IsError() {
		return nil, fmt.Errorf("http error: %d", r.StatusCode())
	}
	if resp.URL == "" {
		return nil, fmt.Errorf("empty download url")
	}
	return &model.Link{URL: resp.URL, Header: http.Header{}}, nil
}

func (d *Wps) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	if parentDir == nil {
		return errs.NotSupport
	}
	node, err := unwrapWpsObj(parentDir)
	if err != nil {
		return err
	}
	if node.Kind != "group" && node.Kind != "folder" {
		return errs.NotSupport
	}
	parentID := int64(0)
	if node.HasFile && node.Kind == "folder" {
		parentID = node.FileID
	}
	body := map[string]interface{}{
		"groupid":  node.GroupID,
		"name":     dirName,
		"parentid": parentID,
	}
	if err := d.doJSON(ctx, http.MethodPost, d.driveURL("/api/v5/files/folder"), body); err != nil {
		return err
	}
	return nil
}

func (d *Wps) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	if srcObj == nil || dstDir == nil {
		return errs.NotSupport
	}
	nodeSrc, err := unwrapWpsObj(srcObj)
	if err != nil {
		return fmt.Errorf("invalid source object type: %w", err)
	}
	nodeDst, err := unwrapWpsObj(dstDir)
	if err != nil {
		return fmt.Errorf("invalid destination object type: %w", err)
	}
	if nodeSrc.Kind != "file" && nodeSrc.Kind != "folder" {
		return errs.NotSupport
	}
	if nodeDst.Kind != "group" && nodeDst.Kind != "folder" {
		return errs.NotSupport
	}
	targetParentID := int64(0)
	if nodeDst.HasFile && nodeDst.Kind == "folder" {
		targetParentID = nodeDst.FileID
	}
	body := map[string]interface{}{
		"fileids":         []int64{nodeSrc.FileID},
		"target_groupid":  nodeDst.GroupID,
		"target_parentid": targetParentID,
	}
	url := fmt.Sprintf("/api/v3/groups/%d/files/batch/move", nodeSrc.GroupID)
	for {
		var res apiResult
		resp, err := d.jsonRequest(ctx).
			SetBody(body).
			SetResult(&res).
			SetError(&res).
			Post(d.driveURL(url))
		if err != nil {
			return err
		}

		if resp.StatusCode() == 403 && res.Result == "fileTaskDuplicated" {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if err := checkAPI(resp, res); err != nil {
			return err
		}
		break
	}
	return nil
}

func (d *Wps) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	if srcObj == nil {
		return errs.NotSupport
	}
	node, err := unwrapWpsObj(srcObj)
	if err != nil {
		return err
	}
	if node.Kind != "file" && node.Kind != "folder" {
		return errs.NotSupport
	}
	url := fmt.Sprintf("/api/v3/groups/%d/files/%d", node.GroupID, node.FileID)
	body := map[string]string{"fname": newName}
	if err := d.doJSON(ctx, http.MethodPut, d.driveURL(url), body); err != nil {
		return err
	}
	return nil
}

func (d *Wps) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	if srcObj == nil || dstDir == nil {
		return errs.NotSupport
	}
	nodeSrc, err := unwrapWpsObj(srcObj)
	if err != nil {
		return fmt.Errorf("invalid source object type: %w", err)
	}
	nodeDst, err := unwrapWpsObj(dstDir)
	if err != nil {
		return fmt.Errorf("invalid destination object type: %w", err)
	}
	if nodeSrc.Kind != "file" && nodeSrc.Kind != "folder" {
		return errs.NotSupport
	}
	if nodeDst.Kind != "group" && nodeDst.Kind != "folder" {
		return errs.NotSupport
	}
	targetParentID := int64(0)
	if nodeDst.HasFile && nodeDst.Kind == "folder" {
		targetParentID = nodeDst.FileID
	}
	body := map[string]interface{}{
		"fileids":               []int64{nodeSrc.FileID},
		"groupid":               nodeSrc.GroupID,
		"target_groupid":        nodeDst.GroupID,
		"target_parentid":       targetParentID,
		"duplicated_name_model": 1,
	}
	url := fmt.Sprintf("/api/v3/groups/%d/files/batch/copy", nodeSrc.GroupID)
	for {
		var res apiResult
		resp, err := d.jsonRequest(ctx).
			SetBody(body).
			SetResult(&res).
			SetError(&res).
			Post(d.driveURL(url))
		if err != nil {
			return err
		}

		if resp.StatusCode() == 403 && res.Result == "fileTaskDuplicated" {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if err := checkAPI(resp, res); err != nil {
			return err
		}
		break
	}
	return nil
}

func (d *Wps) Remove(ctx context.Context, obj model.Obj) error {
	if obj == nil {
		return errs.NotSupport
	}
	node, err := unwrapWpsObj(obj)
	if err != nil {
		return err
	}
	if node.Kind != "file" && node.Kind != "folder" {
		return errs.NotSupport
	}

	body := map[string]interface{}{
		"fileids": []int64{node.FileID},
	}
	url := fmt.Sprintf("/api/v3/groups/%d/files/batch/delete", node.GroupID)

	for {
		var res apiResult
		resp, err := d.jsonRequest(ctx).
			SetBody(body).
			SetResult(&res).
			SetError(&res).
			Post(d.driveURL(url))
		if err != nil {
			return err
		}

		// 无法连续创建文件夹删除。如果一定要删除，每0.5s 尝试一次创建下一个删除请求，应当避免递归删除文件夹
		if resp.StatusCode() == 403 && res.Result == "fileTaskDuplicated" {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if err := checkAPI(resp, res); err != nil {
			return err
		}
		break
	}
	return nil
}

func (d *Wps) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	return d.put(ctx, dstDir, file, up)
}

func (d *Wps) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	url := fmt.Sprintf("%s/api/v3/spaces", d.driveHost()+d.drivePrefix())
	var resp spacesResp
	r, err := d.request(ctx).SetResult(&resp).SetError(&resp).Get(url)
	if err != nil {
		return nil, err
	}
	if r != nil && r.IsError() {
		return nil, fmt.Errorf("http error: %d", r.StatusCode())
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: resp.Total,
			UsedSpace:  resp.Used,
		},
	}, nil
}

var _ driver.Driver = (*Wps)(nil)
