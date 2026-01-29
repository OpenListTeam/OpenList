package wps

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
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

type resolveCacheEntry struct {
	node   *resolvedNode
	expire time.Time
}

type resolveCacheStore struct {
	mu sync.RWMutex
	m  map[string]resolveCacheEntry
}

var resolveCaches sync.Map

type apiResult struct {
	Result string `json:"result"`
	Msg    string `json:"msg"`
}

type uploadCreateUpdateResp struct {
	apiResult
	Method  string `json:"method"`
	URL     string `json:"url"`
	Store   string `json:"store"`
	Request struct {
		Headers  map[string]string `json:"headers"`
		FormData map[string]string `json:"formData"`
	} `json:"request"`
	Response struct {
		ExpectCode []int  `json:"expect_code"`
		ArgsETag   string `json:"args_etag"`
		ArgsKey    string `json:"args_key"`
	} `json:"response"`
}

type uploadPutResp struct {
	NewFilename string `json:"newfilename"`
	Sha1        string `json:"sha1"`
	MD5         string `json:"md5"`
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
	return d.driveHost()
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

func statusOK(code int, expect []int) bool {
	if len(expect) == 0 {
		return code >= 200 && code < 300
	}
	for _, v := range expect {
		if v == code {
			return true
		}
	}
	return false
}

func respArg(arg string, resp *http.Response, body []byte) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return ""
	}
	l := strings.ToLower(arg)
	if strings.HasPrefix(l, "header.") {
		h := strings.TrimSpace(arg[len("header."):])
		if h == "" {
			return ""
		}
		return strings.TrimSpace(resp.Header.Get(h))
	}
	if strings.HasPrefix(l, "body.") {
		k := strings.TrimSpace(arg[len("body."):])
		if k == "" {
			return ""
		}
		var m map[string]interface{}
		if err := json.Unmarshal(body, &m); err != nil {
			return ""
		}
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func extractXMLTag(v, tag string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	lt := strings.ToLower(tag)
	open := "<" + lt + ">"
	clos := "</" + lt + ">"
	ls := strings.ToLower(s)
	i := strings.Index(ls, open)
	if i < 0 {
		return ""
	}
	i += len(open)
	j := strings.Index(ls[i:], clos)
	if j < 0 {
		return ""
	}
	r := strings.TrimSpace(s[i : i+j])
	r = strings.ReplaceAll(r, "&quot;", "")
	return strings.Trim(r, `"'`)
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

func normalizePath(path string) string {
	clean := strings.TrimSpace(path)
	if clean == "" || clean == "/" {
		return "/"
	}
	return "/" + strings.Trim(clean, "/")
}

func (d *Wps) resolveCacheStore() *resolveCacheStore {
	if d == nil {
		return nil
	}
	if v, ok := resolveCaches.Load(d); ok {
		if s, ok := v.(*resolveCacheStore); ok {
			return s
		}
	}
	s := &resolveCacheStore{m: make(map[string]resolveCacheEntry)}
	if v, loaded := resolveCaches.LoadOrStore(d, s); loaded {
		if s2, ok := v.(*resolveCacheStore); ok {
			return s2
		}
	}
	return s
}

func (d *Wps) getResolveCache(path string) (*resolvedNode, bool) {
	s := d.resolveCacheStore()
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	e, ok := s.m[path]
	s.mu.RUnlock()
	if !ok || e.node == nil {
		return nil, false
	}
	if !e.expire.IsZero() && time.Now().After(e.expire) {
		s.mu.Lock()
		delete(s.m, path)
		s.mu.Unlock()
		return nil, false
	}
	return e.node, true
}

func (d *Wps) setResolveCache(path string, node *resolvedNode) {
	s := d.resolveCacheStore()
	if s == nil || node == nil {
		return
	}
	s.mu.Lock()
	s.m[path] = resolveCacheEntry{node: node, expire: time.Now().Add(10 * time.Minute)}
	s.mu.Unlock()
}

func (d *Wps) clearResolveCache() {
	s := d.resolveCacheStore()
	if s == nil {
		return
	}
	s.mu.Lock()
	if len(s.m) != 0 {
		s.m = make(map[string]resolveCacheEntry)
	}
	s.mu.Unlock()
}

func (d *Wps) resolvePath(ctx context.Context, path string) (*resolvedNode, error) {
	cacheKey := normalizePath(path)
	if n, ok := d.getResolveCache(cacheKey); ok {
		return n, nil
	}
	clean := strings.TrimSpace(path)
	if clean == "" {
		clean = "/"
	}
	clean = strings.Trim(clean, "/")
	if clean == "" {
		n := &resolvedNode{kind: "root"}
		d.setResolveCache("/", n)
		return n, nil
	}
	seg := strings.Split(clean, "/")
	groups, err := d.getGroups(ctx)
	if err != nil {
		return nil, err
	}
	var grp *Group
	for i := range groups {
		if groups[i].Name == seg[0] {
			grp = &groups[i]
			break
		}
	}
	if grp == nil {
		return nil, fmt.Errorf("group not found")
	}
	cur := "/" + seg[0]
	gn := &resolvedNode{kind: "group", group: *grp}
	d.setResolveCache(cur, gn)
	if len(seg) == 1 {
		return gn, nil
	}
	parentID := int64(0)
	var lastNode *resolvedNode
	for i := 1; i < len(seg); i++ {
		files, err := d.getFiles(ctx, grp.GroupID, parentID)
		if err != nil {
			return nil, err
		}
		var found *FileInfo
		for j := range files {
			if files[j].Name == seg[i] {
				found = &files[j]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("path not found")
		}
		if i < len(seg)-1 && found.Type != "folder" {
			return nil, fmt.Errorf("path not found")
		}
		fi := *found
		parentID = fi.ID
		cur = cur + "/" + seg[i]
		kind := "file"
		if fi.Type == "folder" {
			kind = "folder"
		}
		n := &resolvedNode{kind: kind, group: *grp, file: &fi}
		d.setResolveCache(cur, n)
		lastNode = n
	}
	if lastNode == nil {
		return nil, fmt.Errorf("path not found")
	}
	return lastNode, nil
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
			d.setResolveCache(normalizePath(path), &resolvedNode{kind: "group", group: g})
		}
		d.setResolveCache("/", &resolvedNode{kind: "root"})
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
		path := normalizePath(joinPath(basePath, f.Name))
		fi := f
		kind := "file"
		if fi.Type == "folder" {
			kind = "folder"
		}
		d.setResolveCache(path, &resolvedNode{kind: kind, group: node.group, file: &fi})
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
	if err := d.doJSON(ctx, http.MethodPost, d.driveURL("/api/v5/files/folder"), body); err != nil {
		return err
	}
	d.clearResolveCache()
	return nil
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
	d.clearResolveCache()
	return nil
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
	if err := d.doJSON(ctx, http.MethodPut, d.driveURL(url), body); err != nil {
		return err
	}
	d.clearResolveCache()
	return nil
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
	d.clearResolveCache()
	return nil
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
	d.clearResolveCache()
	return nil
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

func (d *Wps) commitUpload(ctx context.Context, etag, key string, groupID, parentID int64, name, sha1Hex string, size int64, store string) error {
	store = strings.TrimSpace(store)
	if store == "" {
		store = "ks3"
	}
	storeKey := ""
	if key != "" {
		storeKey = key
	}
	body := map[string]interface{}{
		"etag":     etag,
		"groupid":  groupID,
		"key":      key,
		"name":     name,
		"parentid": parentID,
		"sha1":     sha1Hex,
		"size":     size,
		"store":    store,
		"storekey": storeKey,
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

	// 在隐藏文件名前加_上传，这是WPS的限制，无法上传隐藏文件，也无法将任何文件重命名为隐藏文件，所有隐藏文件会被自动加上_ 上传
	// 甚至可以上传前缀是..的文件，但是单个点就是不行
	realName := file.GetName()
	uploadName := realName
	if strings.HasPrefix(realName, ".") {
		uploadName = "_" + realName
	}

	info, err := d.createUpload(ctx, node.group.GroupID, parentID, uploadName, size, sha1Hex, sha256Hex)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	rf := driver.NewLimitedUploadFile(ctx, f)
	prog := driver.NewProgress(size, model.UpdateProgressWithRange(up, 0, 1))

	method := strings.ToUpper(strings.TrimSpace(info.Method))
	if method == "" {
		method = http.MethodPut
	}

	var req *http.Request
	if method == http.MethodPost && len(info.Request.FormData) > 0 {
		if size == 0 {
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			for k, v := range info.Request.FormData {
				if err := mw.WriteField(k, v); err != nil {
					return err
				}
			}
			part, err := mw.CreateFormFile("file", uploadName)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, io.TeeReader(rf, prog)); err != nil {
				return err
			}
			if err := mw.Close(); err != nil {
				return err
			}
			req, err = http.NewRequestWithContext(ctx, method, info.URL, bytes.NewReader(buf.Bytes()))
			if err != nil {
				return err
			}
			for k, v := range info.Request.Headers {
				req.Header.Set(k, v)
			}
			req.Header.Set("Content-Type", mw.FormDataContentType())
			req.ContentLength = int64(buf.Len())
			req.Header.Set("Content-Length", strconv.FormatInt(req.ContentLength, 10))
		} else {
			pr, pw := io.Pipe()
			mw := multipart.NewWriter(pw)
			req, err = http.NewRequestWithContext(ctx, method, info.URL, pr)
			if err != nil {
				return err
			}
			for k, v := range info.Request.Headers {
				req.Header.Set(k, v)
			}
			req.Header.Set("Content-Type", mw.FormDataContentType())
			go func() {
				for k, v := range info.Request.FormData {
					if err := mw.WriteField(k, v); err != nil {
						pw.CloseWithError(err)
						return
					}
				}
				part, err := mw.CreateFormFile("file", uploadName)
				if err != nil {
					pw.CloseWithError(err)
					return
				}
				if _, err := io.Copy(part, io.TeeReader(rf, prog)); err != nil {
					pw.CloseWithError(err)
					return
				}
				if err := mw.Close(); err != nil {
					pw.CloseWithError(err)
					return
				}
				pw.Close()
			}()
		}
	} else {
		var body = io.TeeReader(rf, prog)
		if size == 0 {
			body = bytes.NewReader(nil)
		}
		req, err = http.NewRequestWithContext(ctx, method, info.URL, body)
		if err != nil {
			return err
		}
		for k, v := range info.Request.Headers {
			req.Header.Set(k, v)
		}
		req.ContentLength = size
		req.Header.Set("Content-Length", strconv.FormatInt(size, 10))
	}

	c := *base.RestyClient.GetClient()
	c.Timeout = 0
	resp, err := (&c).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if !statusOK(resp.StatusCode, info.Response.ExpectCode) {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("http error: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	etag := normalizeETag(respArg(info.Response.ArgsETag, resp, body))
	if etag == "" {
		etag = normalizeETag(resp.Header.Get("ETag"))
	}

	key := strings.TrimSpace(respArg(info.Response.ArgsKey, resp, body))
	if key == "" {
		key = strings.TrimSpace(resp.Header.Get("x-obs-save-key"))
	}

	var pr uploadPutResp
	sha1FromServer := ""
	if err := json.Unmarshal(body, &pr); err == nil {
		sha1FromServer = strings.TrimSpace(pr.NewFilename)
		if sha1FromServer == "" {
			sha1FromServer = strings.TrimSpace(pr.Sha1)
		}
		if etag == "" && pr.MD5 != "" {
			etag = strings.TrimSpace(pr.MD5)
		}
	}

	if sha1FromServer == "" {
		if v := extractXMLTag(string(body), "ETag"); v != "" {
			sha1FromServer = v
			if etag == "" {
				etag = v
			}
		}
	}
	if sha1FromServer == "" && key != "" && len(key) == 40 {
		sha1FromServer = key
	}
	if sha1FromServer == "" {
		sha1FromServer = sha1Hex
	}

	if etag == "" {
		return fmt.Errorf("empty etag")
	}
	if sha1FromServer == "" {
		return fmt.Errorf("empty sha1")
	}

	store := strings.TrimSpace(info.Store)
	commitKey := ""
	if strings.TrimSpace(info.Response.ArgsKey) != "" {
		commitKey = key
		if commitKey == "" {
			commitKey = sha1FromServer
		}
	}

	if err := d.commitUpload(ctx, etag, commitKey, node.group.GroupID, parentID, uploadName, sha1FromServer, size, store); err != nil {
		return err
	}

	up(1)
	return nil
}

func (d *Wps) spaces(ctx context.Context) (*spacesResp, error) {
	url := fmt.Sprintf("%s/api/v3/spaces", d.driveHost()+d.drivePrefix())
	var resp spacesResp
	r, err := d.request(ctx).SetResult(&resp).SetError(&resp).Get(url)
	if err != nil {
		return nil, err
	}
	if r != nil && r.IsError() {
		return nil, fmt.Errorf("http error: %d", r.StatusCode())
	}
	return &resp, nil
}
