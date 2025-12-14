package wps

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

const endpoint = "https://365.kdocs.cn"
const personalEndpoint = "https://drive.wps.cn"

type resolvedNode struct {
	kind  string
	group Group
	file  *FileInfo
}

type apiResult struct {
	Result string `json:"result"`
	Msg    string `json:"msg"`
}

type uploadCreateUpdateResp struct {
	apiResult
	Method  string `json:"method"`
	URL     string `json:"url"`
	Request struct {
		Headers map[string]string `json:"headers"`
	} `json:"request"`
}

type uploadPutResp struct {
	NewFilename string `json:"newfilename"`
}

type personalGroupsResp struct {
	apiResult
	Groups []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"groups"`
}

type countingWriter struct {
	n *int64
}

func (w countingWriter) Write(p []byte) (int, error) {
	*w.n += int64(len(p))
	return len(p), nil
}

func (d *Wps) isPersonal() bool {
	return strings.TrimSpace(d.Mode) == "Personal"
}

func (d *Wps) driveHost() string {
	if d.isPersonal() {
		return personalEndpoint
	}
	return endpoint
}

func (d *Wps) drivePrefix() string {
	if d.isPersonal() {
		return ""
	}
	return "/3rd/drive"
}

func (d *Wps) driveURL(path string) string {
	return d.driveHost() + d.drivePrefix() + path
}

func (d *Wps) origin() string {
	return d.driveHost() + "/"
}

func (d *Wps) canDownload(f *FileInfo) bool {
	if f == nil || f.Type == "folder" {
		return false
	}
	if f.FilePerms.Download != 0 {
		return true
	}
	return d.isPersonal()
}

func (d *Wps) request(ctx context.Context) *resty.Request {
	return base.RestyClient.R().
		SetHeader("Cookie", d.Cookie).
		SetHeader("Accept", "application/json").
		SetContext(ctx)
}

func (d *Wps) jsonRequest(ctx context.Context) *resty.Request {
	return d.request(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("Origin", d.origin())
}

func checkAPI(resp *resty.Response, result apiResult) error {
	if result.Result != "" && result.Result != "ok" {
		if result.Msg == "" {
			result.Msg = "unknown error"
		}
		return fmt.Errorf("%s: %s", result.Result, result.Msg)
	}
	if resp != nil && resp.IsError() {
		if result.Msg != "" {
			return fmt.Errorf("%s", result.Msg)
		}
		return fmt.Errorf("http error: %d", resp.StatusCode())
	}
	return nil
}

func (d *Wps) ensureCompanyID(ctx context.Context) error {
	if d.isPersonal() {
		return nil
	}
	if d.companyID != "" {
		return nil
	}
	var resp workspaceResp
	r, err := d.request(ctx).SetResult(&resp).SetError(&resp).Get(endpoint + "/3rd/plussvr/compose/v1/users/self/workspaces?fields=name&comp_status=active")
	if err != nil {
		return err
	}
	if r != nil && r.IsError() {
		return fmt.Errorf("http error: %d", r.StatusCode())
	}
	if len(resp.Companies) == 0 {
		return fmt.Errorf("no company id")
	}
	d.companyID = strconv.FormatInt(resp.Companies[0].ID, 10)
	return nil
}

func (d *Wps) getGroups(ctx context.Context) ([]Group, error) {
	if d.isPersonal() {
		var resp personalGroupsResp
		r, err := d.request(ctx).SetResult(&resp).SetError(&resp).Get(d.driveURL("/api/v3/groups"))
		if err != nil {
			return nil, err
		}
		if err := checkAPI(r, resp.apiResult); err != nil {
			return nil, err
		}
		res := make([]Group, 0, len(resp.Groups))
		for _, g := range resp.Groups {
			res = append(res, Group{GroupID: g.ID, Name: g.Name})
		}
		return res, nil
	}
	if err := d.ensureCompanyID(ctx); err != nil {
		return nil, err
	}
	var resp groupsResp
	url := fmt.Sprintf("%s/3rd/plus/groups/v1/companies/%s/users/self/groups/private", endpoint, d.companyID)
	r, err := d.request(ctx).SetResult(&resp).SetError(&resp).Get(url)
	if err != nil {
		return nil, err
	}
	if r != nil && r.IsError() {
		return nil, fmt.Errorf("http error: %d", r.StatusCode())
	}
	return resp.Groups, nil
}

func (d *Wps) getFiles(ctx context.Context, groupID, parentID int64) ([]FileInfo, error) {
	var resp filesResp
	url := fmt.Sprintf("%s/api/v5/groups/%d/files", d.driveHost()+d.drivePrefix(), groupID)
	r, err := d.request(ctx).
		SetQueryParam("parentid", strconv.FormatInt(parentID, 10)).
		SetResult(&resp).
		SetError(&resp).
		Get(url)
	if err != nil {
		return nil, err
	}
	if r != nil && r.IsError() {
		return nil, fmt.Errorf("http error: %d", r.StatusCode())
	}
	return resp.Files, nil
}

func parseTime(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}

func joinPath(basePath, name string) string {
	if basePath == "" || basePath == "/" {
		return "/" + name
	}
	return strings.TrimRight(basePath, "/") + "/" + name
}

func (d *Wps) resolvePath(ctx context.Context, path string) (*resolvedNode, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		clean = "/"
	}
	clean = strings.Trim(clean, "/")
	if clean == "" {
		return &resolvedNode{kind: "root"}, nil
	}
	segs := strings.Split(clean, "/")
	groups, err := d.getGroups(ctx)
	if err != nil {
		return nil, err
	}
	var grp *Group
	for i := range groups {
		if groups[i].Name == segs[0] {
			grp = &groups[i]
			break
		}
	}
	if grp == nil {
		return nil, fmt.Errorf("group not found")
	}
	if len(segs) == 1 {
		return &resolvedNode{kind: "group", group: *grp}, nil
	}
	parentID := int64(0)
	var last FileInfo
	for i := 1; i < len(segs); i++ {
		files, err := d.getFiles(ctx, grp.GroupID, parentID)
		if err != nil {
			return nil, err
		}
		var found *FileInfo
		for j := range files {
			if files[j].Name == segs[i] {
				found = &files[j]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("path not found")
		}
		if i < len(segs)-1 && found.Type != "folder" {
			return nil, fmt.Errorf("path not found")
		}
		last = *found
		parentID = found.ID
	}
	kind := "file"
	if last.Type == "folder" {
		kind = "folder"
	}
	return &resolvedNode{kind: kind, group: *grp, file: &last}, nil
}

func (d *Wps) fileToObj(basePath string, f FileInfo) *Obj {
	name := f.Name
	path := joinPath(basePath, name)
	obj := &Obj{
		id:    path,
		name:  name,
		size:  f.Size,
		ctime: parseTime(f.Ctime),
		mtime: parseTime(f.Mtime),
		isDir: f.Type == "folder",
		path:  path,
	}
	if !obj.isDir {
		obj.canDownload = d.canDownload(&f)
	}
	return obj
}

func (d *Wps) doJSON(ctx context.Context, method, url string, body interface{}) error {
	var result apiResult
	req := d.jsonRequest(ctx).SetBody(body).SetResult(&result).SetError(&result)
	var (
		resp *resty.Response
		err  error
	)
	switch method {
	case http.MethodPost:
		resp, err = req.Post(url)
	case http.MethodPut:
		resp, err = req.Put(url)
	default:
		return errs.NotSupport
	}
	if err != nil {
		return err
	}
	return checkAPI(resp, result)
}

func (d *Wps) list(ctx context.Context, basePath string) ([]model.Obj, error) {
	if strings.TrimSpace(basePath) == "" {
		basePath = "/"
	}
	node, err := d.resolvePath(ctx, basePath)
	if err != nil {
		return nil, err
	}
	if node.kind == "root" {
		groups, err := d.getGroups(ctx)
		if err != nil {
			return nil, err
		}
		res := make([]model.Obj, 0, len(groups))
		for _, g := range groups {
			path := joinPath(basePath, g.Name)
			obj := &Obj{
				id:    path,
				name:  g.Name,
				ctime: parseTime(0),
				mtime: parseTime(0),
				isDir: true,
				path:  path,
			}
			res = append(res, obj)
		}
		return res, nil
	}
	if node.kind != "group" && node.kind != "folder" {
		return nil, nil
	}
	parentID := int64(0)
	if node.file != nil && node.kind == "folder" {
		parentID = node.file.ID
	}
	files, err := d.getFiles(ctx, node.group.GroupID, parentID)
	if err != nil {
		return nil, err
	}
	res := make([]model.Obj, 0, len(files))
	for _, f := range files {
		res = append(res, d.fileToObj(basePath, f))
	}
	return res, nil
}

func (d *Wps) link(ctx context.Context, path string) (*model.Link, error) {
	node, err := d.resolvePath(ctx, path)
	if err != nil {
		return nil, err
	}
	if node.kind != "file" || node.file == nil {
		return nil, errs.NotSupport
	}
	if !d.canDownload(node.file) {
		return nil, fmt.Errorf("no download permission")
	}
	url := fmt.Sprintf("%s/api/v5/groups/%d/files/%d/download?support_checksums=sha1", d.driveHost()+d.drivePrefix(), node.group.GroupID, node.file.ID)
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

func (d *Wps) makeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	if parentDir == nil {
		return errs.NotSupport
	}
	node, err := d.resolvePath(ctx, parentDir.GetPath())
	if err != nil {
		return err
	}
	if node.kind != "group" && node.kind != "folder" {
		return errs.NotSupport
	}
	parentID := int64(0)
	if node.file != nil && node.kind == "folder" {
		parentID = node.file.ID
	}
	body := map[string]interface{}{
		"groupid":  node.group.GroupID,
		"name":     dirName,
		"parentid": parentID,
	}
	return d.doJSON(ctx, http.MethodPost, d.driveURL("/api/v5/files/folder"), body)
}

func (d *Wps) move(ctx context.Context, srcObj, dstDir model.Obj) error {
	if srcObj == nil || dstDir == nil {
		return errs.NotSupport
	}
	nodeSrc, err := d.resolvePath(ctx, srcObj.GetPath())
	if err != nil {
		return err
	}
	nodeDst, err := d.resolvePath(ctx, dstDir.GetPath())
	if err != nil {
		return err
	}
	if nodeSrc.kind != "file" && nodeSrc.kind != "folder" {
		return errs.NotSupport
	}
	if nodeDst.kind != "group" && nodeDst.kind != "folder" {
		return errs.NotSupport
	}
	targetParentID := int64(0)
	if nodeDst.file != nil && nodeDst.kind == "folder" {
		targetParentID = nodeDst.file.ID
	}
	body := map[string]interface{}{
		"fileids":         []int64{nodeSrc.file.ID},
		"target_groupid":  nodeDst.group.GroupID,
		"target_parentid": targetParentID,
	}
	url := fmt.Sprintf("/api/v3/groups/%d/files/batch/move", nodeSrc.group.GroupID)
	return d.doJSON(ctx, http.MethodPost, d.driveURL(url), body)
}

func (d *Wps) rename(ctx context.Context, srcObj model.Obj, newName string) error {
	if srcObj == nil {
		return errs.NotSupport
	}
	node, err := d.resolvePath(ctx, srcObj.GetPath())
	if err != nil {
		return err
	}
	if node.kind != "file" && node.kind != "folder" {
		return errs.NotSupport
	}
	url := fmt.Sprintf("/api/v3/groups/%d/files/%d", node.group.GroupID, node.file.ID)
	body := map[string]string{"fname": newName}
	return d.doJSON(ctx, http.MethodPut, d.driveURL(url), body)
}

func (d *Wps) copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	if srcObj == nil || dstDir == nil {
		return errs.NotSupport
	}
	nodeSrc, err := d.resolvePath(ctx, srcObj.GetPath())
	if err != nil {
		return err
	}
	nodeDst, err := d.resolvePath(ctx, dstDir.GetPath())
	if err != nil {
		return err
	}
	if nodeSrc.kind != "file" && nodeSrc.kind != "folder" {
		return errs.NotSupport
	}
	if nodeDst.kind != "group" && nodeDst.kind != "folder" {
		return errs.NotSupport
	}
	targetParentID := int64(0)
	if nodeDst.file != nil && nodeDst.kind == "folder" {
		targetParentID = nodeDst.file.ID
	}
	body := map[string]interface{}{
		"fileids":               []int64{nodeSrc.file.ID},
		"groupid":               nodeSrc.group.GroupID,
		"target_groupid":        nodeDst.group.GroupID,
		"target_parentid":       targetParentID,
		"duplicated_name_model": 1,
	}
	url := fmt.Sprintf("/api/v3/groups/%d/files/batch/copy", nodeSrc.group.GroupID)
	return d.doJSON(ctx, http.MethodPost, d.driveURL(url), body)
}

func (d *Wps) remove(ctx context.Context, obj model.Obj) error {
	if obj == nil {
		return errs.NotSupport
	}
	node, err := d.resolvePath(ctx, obj.GetPath())
	if err != nil {
		return err
	}
	if node.kind != "file" && node.kind != "folder" {
		return errs.NotSupport
	}
	body := map[string]interface{}{
		"fileids": []int64{node.file.ID},
	}
	url := fmt.Sprintf("/api/v3/groups/%d/files/batch/delete", node.group.GroupID)
	return d.doJSON(ctx, http.MethodPost, d.driveURL(url), body)
}

func cacheAndHash(file model.FileStreamer, up driver.UpdateProgress) (model.File, int64, string, string, error) {
	h1 := sha1.New()
	h256 := sha256.New()
	size := file.GetSize()
	var counted int64
	ws := []io.Writer{h1, h256}
	if size <= 0 {
		ws = append(ws, countingWriter{n: &counted})
	}
	p := up
	f, err := file.CacheFullAndWriter(&p, io.MultiWriter(ws...))
	if err != nil {
		return nil, 0, "", "", err
	}
	if size <= 0 {
		size = counted
	}
	return f, size, hex.EncodeToString(h1.Sum(nil)), hex.EncodeToString(h256.Sum(nil)), nil
}

func (d *Wps) createUpload(ctx context.Context, groupID, parentID int64, name string, size int64, sha1Hex, sha256Hex string) (*uploadCreateUpdateResp, error) {
	body := map[string]string{
		"group_id":  strconv.FormatInt(groupID, 10),
		"name":      name,
		"parent_id": strconv.FormatInt(parentID, 10),
		"sha1":      sha1Hex,
		"sha256":    sha256Hex,
		"size":      strconv.FormatInt(size, 10),
	}
	var resp uploadCreateUpdateResp
	r, err := d.jsonRequest(ctx).
		SetBody(body).
		SetResult(&resp).
		SetError(&resp).
		Put(d.driveURL("/api/v5/files/upload/create_update"))
	if err != nil {
		return nil, err
	}
	if err := checkAPI(r, resp.apiResult); err != nil {
		return nil, err
	}
	if resp.URL == "" {
		return nil, fmt.Errorf("empty upload url")
	}
	return &resp, nil
}

func normalizeETag(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "W/") {
		v = strings.TrimSpace(strings.TrimPrefix(v, "W/"))
	}
	return strings.Trim(v, `"`)
}

func (d *Wps) commitUpload(ctx context.Context, etag string, groupID, parentID int64, name, sha1Hex string, size int64) error {
	body := map[string]interface{}{
		"etag":     etag,
		"groupid":  groupID,
		"key":      "",
		"name":     name,
		"parentid": parentID,
		"sha1":     sha1Hex,
		"size":     size,
		"store":    "ks3",
		"storekey": "",
	}
	return d.doJSON(ctx, http.MethodPost, d.driveURL("/api/v5/files/file"), body)
}

func (d *Wps) put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	if dstDir == nil || file == nil {
		return errs.NotSupport
	}
	if up == nil {
		up = func(float64) {}
	}
	node, err := d.resolvePath(ctx, dstDir.GetPath())
	if err != nil {
		return err
	}
	if node.kind != "group" && node.kind != "folder" {
		return errs.NotSupport
	}
	parentID := int64(0)
	if node.file != nil && node.kind == "folder" {
		parentID = node.file.ID
	}
	f, size, sha1Hex, sha256Hex, err := cacheAndHash(file, func(float64) {})
	if err != nil {
		return err
	}
	if c, ok := f.(io.Closer); ok {
		defer c.Close()
	}
	info, err := d.createUpload(ctx, node.group.GroupID, parentID, file.GetName(), size, sha1Hex, sha256Hex)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	rf := driver.NewLimitedUploadFile(ctx, f)
	prog := driver.NewProgress(size, model.UpdateProgressWithRange(up, 0, 1))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, info.URL, io.TeeReader(rf, prog))
	if err != nil {
		return err
	}
	req.ContentLength = size
	for k, v := range info.Request.Headers {
		req.Header.Set(k, v)
	}
	method := strings.ToUpper(strings.TrimSpace(info.Method))
	if method != "" && method != http.MethodPut {
		req.Method = method
	}
	c := *base.RestyClient.GetClient()
	c.Timeout = 0
	resp, err := (&c).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("http error: %d", resp.StatusCode)
	}
	etag := normalizeETag(resp.Header.Get("ETag"))
	var pr uploadPutResp
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return err
	}
	sha1FromServer := strings.TrimSpace(pr.NewFilename)
	if etag == "" {
		return fmt.Errorf("empty etag")
	}
	if sha1FromServer == "" {
		return fmt.Errorf("empty newfilename")
	}
	if err := d.commitUpload(ctx, etag, node.group.GroupID, parentID, file.GetName(), sha1FromServer, size); err != nil {
		return err
	}
	up(1)
	return nil
}
