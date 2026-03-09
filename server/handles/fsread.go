package handles

import (
	"fmt"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type ListReq struct {
	model.PageReq
	Path     string `json:"path" form:"path"`
	Password string `json:"password" form:"password"`
	Refresh  bool   `json:"refresh"`
}

type DirReq struct {
	Path      string `json:"path" form:"path"`
	Password  string `json:"password" form:"password"`
	ForceRoot bool   `json:"force_root" form:"force_root"`
}

type ObjResp struct {
	Name         string                     `json:"name"`
	Size         int64                      `json:"size"`
	IsDir        bool                       `json:"is_dir"`
	Modified     time.Time                  `json:"modified"`
	Created      time.Time                  `json:"created"`
	Sign         string                     `json:"sign"`
	Thumb        string                     `json:"thumb"`
	Type         int                        `json:"type"`
	HashInfoStr  string                     `json:"hashinfo"`
	HashInfo     map[*utils.HashType]string `json:"hash_info"`
	MountDetails *model.StorageDetails      `json:"mount_details,omitempty"`
}

type FsListResp struct {
	Content           []ObjResp `json:"content"`
	Total             int64     `json:"total"`
	Readme            string    `json:"readme"`
	Header            string    `json:"header"`
	Write             bool      `json:"write"`
	Provider          string    `json:"provider"`
	DirectUploadTools []string  `json:"direct_upload_tools,omitempty"`
}

func FsListSplit(c *gin.Context) {
	var req ListReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	req.Validate()
	if strings.HasPrefix(req.Path, "/@s") {
		req.Path = strings.TrimPrefix(req.Path, "/@s")
		SharingList(c, &req)
		return
	}
	// 虚拟主机路径重映射：根据 Host 头匹配虚拟主机规则，将请求路径映射到实际路径
	req.Path = applyVhostPathMapping(c, req.Path)
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if user.IsGuest() && user.Disabled {
		common.ErrorStrResp(c, "Guest user is disabled, login please", 401)
		return
	}
	FsList(c, &req, user)
}

func FsList(c *gin.Context, req *ListReq, user *model.User) {
	reqPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorResp(c, err, 500, true)
			return
		}
	}
	common.GinWithValue(c, conf.MetaKey, meta)
	if !common.CanAccess(user, meta, reqPath, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	if !user.CanWrite() && !common.CanWrite(meta, reqPath) && req.Refresh {
		common.ErrorStrResp(c, "Refresh without permission", 403)
		return
	}
	objs, err := fs.List(c.Request.Context(), reqPath, &fs.ListArgs{
		Refresh:            req.Refresh,
		WithStorageDetails: !user.IsGuest() && !setting.GetBool(conf.HideStorageDetails),
	})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	total, objs := pagination(objs, &req.PageReq)
	provider := "unknown"
	var directUploadTools []string
	if user.CanWrite() {
		if storage, err := fs.GetStorage(reqPath, &fs.GetStoragesArgs{}); err == nil {
			directUploadTools = op.GetDirectUploadTools(storage)
		}
	}
	common.SuccessResp(c, FsListResp{
		Content:           toObjsResp(objs, reqPath, isEncrypt(meta, reqPath)),
		Total:             int64(total),
		Readme:            getReadme(meta, reqPath),
		Header:            getHeader(meta, reqPath),
		Write:             user.CanWrite() || common.CanWrite(meta, reqPath),
		Provider:          provider,
		DirectUploadTools: directUploadTools,
	})
}

func FsDirs(c *gin.Context) {
	var req DirReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	reqPath := req.Path
	if req.ForceRoot {
		if !user.IsAdmin() {
			common.ErrorStrResp(c, "Permission denied", 403)
			return
		}
	} else {
		tmp, err := user.JoinPath(req.Path)
		if err != nil {
			common.ErrorResp(c, err, 403)
			return
		}
		reqPath = tmp
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorResp(c, err, 500, true)
			return
		}
	}
	common.GinWithValue(c, conf.MetaKey, meta)
	if !common.CanAccess(user, meta, reqPath, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	objs, err := fs.List(c.Request.Context(), reqPath, &fs.ListArgs{})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	dirs := filterDirs(objs)
	common.SuccessResp(c, dirs)
}

type DirResp struct {
	Name     string    `json:"name"`
	Modified time.Time `json:"modified"`
}

func filterDirs(objs []model.Obj) []DirResp {
	var dirs []DirResp
	for _, obj := range objs {
		if obj.IsDir() {
			dirs = append(dirs, DirResp{
				Name:     obj.GetName(),
				Modified: obj.ModTime(),
			})
		}
	}
	return dirs
}

func getReadme(meta *model.Meta, path string) string {
	if meta != nil && (utils.PathEqual(meta.Path, path) || meta.RSub) {
		return meta.Readme
	}
	return ""
}

func getHeader(meta *model.Meta, path string) string {
	if meta != nil && (utils.PathEqual(meta.Path, path) || meta.HeaderSub) {
		return meta.Header
	}
	return ""
}

func isEncrypt(meta *model.Meta, path string) bool {
	if common.IsStorageSignEnabled(path) {
		return true
	}
	if meta == nil || meta.Password == "" {
		return false
	}
	if !utils.PathEqual(meta.Path, path) && !meta.PSub {
		return false
	}
	return true
}

func pagination(objs []model.Obj, req *model.PageReq) (int, []model.Obj) {
	pageIndex, pageSize := req.Page, req.PerPage
	total := len(objs)
	start := (pageIndex - 1) * pageSize
	if start > total {
		return total, []model.Obj{}
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return total, objs[start:end]
}

func toObjsResp(objs []model.Obj, parent string, encrypt bool) []ObjResp {
	var resp []ObjResp
	for _, obj := range objs {
		thumb, _ := model.GetThumb(obj)
		mountDetails, _ := model.GetStorageDetails(obj)
		resp = append(resp, ObjResp{
			Name:         obj.GetName(),
			Size:         obj.GetSize(),
			IsDir:        obj.IsDir(),
			Modified:     obj.ModTime(),
			Created:      obj.CreateTime(),
			HashInfoStr:  obj.GetHash().String(),
			HashInfo:     obj.GetHash().Export(),
			Sign:         common.Sign(obj, parent, encrypt),
			Thumb:        thumb,
			Type:         utils.GetObjType(obj.GetName(), obj.IsDir()),
			MountDetails: mountDetails,
		})
	}
	return resp
}

type FsGetReq struct {
	Path     string `json:"path" form:"path"`
	Password string `json:"password" form:"password"`
}

type FsGetResp struct {
	ObjResp
	RawURL   string    `json:"raw_url"`
	Readme   string    `json:"readme"`
	Header   string    `json:"header"`
	Provider string    `json:"provider"`
	Related  []ObjResp `json:"related"`
}

func FsGetSplit(c *gin.Context) {
	var req FsGetReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if strings.HasPrefix(req.Path, "/@s") {
		req.Path = strings.TrimPrefix(req.Path, "/@s")
		SharingGet(c, &req)
		return
	}
	// 虚拟主机路径重映射：根据 Host 头匹配虚拟主机规则，将请求路径映射到实际路径
	// 同时将 vhost.Path 前缀存入 context，供 FsGet 生成 /p/ 链接时去掉前缀
	var vhostPrefix string
	req.Path, vhostPrefix = applyVhostPathMappingWithPrefix(c, req.Path)
	common.GinWithValue(c, conf.VhostPrefixKey, vhostPrefix)
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if user.IsGuest() && user.Disabled {
		common.ErrorStrResp(c, "Guest user is disabled, login please", 401)
		return
	}
	FsGet(c, &req, user)
}

func FsGet(c *gin.Context, req *FsGetReq, user *model.User) {
	reqPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorResp(c, err, 500)
			return
		}
	}
	common.GinWithValue(c, conf.MetaKey, meta)
	if !common.CanAccess(user, meta, reqPath, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	obj, err := fs.Get(c.Request.Context(), reqPath, &fs.GetArgs{
		WithStorageDetails: !user.IsGuest() && !setting.GetBool(conf.HideStorageDetails),
	})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	var rawURL string

	storage, err := fs.GetStorage(reqPath, &fs.GetStoragesArgs{})
	provider, ok := model.GetProvider(obj)
	if !ok && err == nil {
		provider = storage.Config().Name
	}
	if !obj.IsDir() {
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
		if storage.Config().MustProxy() || storage.GetStorage().WebProxy {
			rawURL = common.GenerateDownProxyURL(storage.GetStorage(), reqPath)
			if rawURL == "" {
				query := ""
				// 生成 /p/ 链接时，去掉 vhost 路径前缀，保持前端看到的路径一致
				downPath := stripVhostPrefix(c, reqPath)
				if isEncrypt(meta, reqPath) || setting.GetBool(conf.SignAll) {
					query = "?sign=" + sign.Sign(reqPath)
				}
				rawURL = fmt.Sprintf("%s/p%s%s",
					common.GetApiUrl(c),
					utils.EncodePath(downPath, true),
					query)
			}
		} else {
			// file have raw url
			if url, ok := model.GetUrl(obj); ok {
				rawURL = url
			} else {
				// if storage is not proxy, use raw url by fs.Link
				link, _, err := fs.Link(c.Request.Context(), reqPath, model.LinkArgs{
					IP:       c.ClientIP(),
					Header:   c.Request.Header,
					Redirect: true,
				})
				if err != nil {
					common.ErrorResp(c, err, 500)
					return
				}
				defer link.Close()
				rawURL = link.URL
			}
		}
	}
	var related []model.Obj
	parentPath := stdpath.Dir(reqPath)
	sameLevelFiles, err := fs.List(c.Request.Context(), parentPath, &fs.ListArgs{})
	if err == nil {
		related = filterRelated(sameLevelFiles, obj)
	}
	parentMeta, _ := op.GetNearestMeta(parentPath)
	thumb, _ := model.GetThumb(obj)
	mountDetails, _ := model.GetStorageDetails(obj)
	common.SuccessResp(c, FsGetResp{
		ObjResp: ObjResp{
			Name:         obj.GetName(),
			Size:         obj.GetSize(),
			IsDir:        obj.IsDir(),
			Modified:     obj.ModTime(),
			Created:      obj.CreateTime(),
			HashInfoStr:  obj.GetHash().String(),
			HashInfo:     obj.GetHash().Export(),
			Sign:         common.Sign(obj, parentPath, isEncrypt(meta, reqPath)),
			Type:         utils.GetFileType(obj.GetName()),
			Thumb:        thumb,
			MountDetails: mountDetails,
		},
		RawURL:   rawURL,
		Readme:   getReadme(meta, reqPath),
		Header:   getHeader(meta, reqPath),
		Provider: provider,
		Related:  toObjsResp(related, parentPath, isEncrypt(parentMeta, parentPath)),
	})
}

func filterRelated(objs []model.Obj, obj model.Obj) []model.Obj {
	var related []model.Obj
	nameWithoutExt := strings.TrimSuffix(obj.GetName(), stdpath.Ext(obj.GetName()))
	for _, o := range objs {
		if o.GetName() == obj.GetName() {
			continue
		}
		if strings.HasPrefix(o.GetName(), nameWithoutExt) {
			related = append(related, o)
		}
	}
	return related
}

type FsOtherReq struct {
	model.FsOtherArgs
	Password string `json:"password" form:"password"`
}

func FsOther(c *gin.Context) {
	var req FsOtherReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	var err error
	req.Path, err = user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(req.Path)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorResp(c, err, 500)
			return
		}
	}
	common.GinWithValue(c, conf.MetaKey, meta)
	if !common.CanAccess(user, meta, req.Path, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	res, err := fs.Other(c.Request.Context(), req.FsOtherArgs)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, res)
}

// applyVhostPathMapping 根据请求的 Host 头匹配虚拟主机规则，将请求路径映射到实际路径。
func applyVhostPathMapping(c *gin.Context, reqPath string) string {
	mapped, _ := applyVhostPathMappingWithPrefix(c, reqPath)
	return mapped
}

// applyVhostPathMappingWithPrefix 根据请求的 Host 头匹配虚拟主机规则，
// 将请求路径映射到虚拟主机配置的实际路径，同时返回 vhost.Path 前缀（用于生成下载链接时去掉前缀）。
// 例如：vhost.Path="/123pan/Downloads"，reqPath="/"，则返回 ("/123pan/Downloads", "/123pan/Downloads")
// 例如：vhost.Path="/123pan/Downloads"，reqPath="/subdir"，则返回 ("/123pan/Downloads/subdir", "/123pan/Downloads")
// 如果没有匹配的虚拟主机规则，则返回 (原始路径, "")
func applyVhostPathMappingWithPrefix(c *gin.Context, reqPath string) (string, string) {
	rawHost := c.Request.Host
	domain := stripHostPortForVhost(rawHost)
	if domain == "" {
		return reqPath, ""
	}
	vhost, err := op.GetVirtualHostByDomain(domain)
	if err != nil || vhost == nil {
		return reqPath, ""
	}
	if !vhost.Enabled || vhost.WebHosting {
		// 未启用，或者是 Web 托管模式（Web 托管不做路径重映射）
		return reqPath, ""
	}
	// 路径重映射：将 reqPath 拼接到 vhost.Path 后面
	mapped := stdpath.Join(vhost.Path, reqPath)
	utils.Log.Debugf("[VirtualHost] API path remapping: domain=%q reqPath=%q -> mappedPath=%q", domain, reqPath, mapped)
	return mapped, vhost.Path
}

// stripVhostPrefix 从 gin context 中取出 vhost 路径前缀，并从 path 中去掉该前缀。
// 用于生成 /p/ 下载链接时，将真实路径还原为前端看到的路径。
func stripVhostPrefix(c *gin.Context, path string) string {
	prefix, ok := c.Request.Context().Value(conf.VhostPrefixKey).(string)
	if !ok || prefix == "" {
		return path
	}
	if strings.HasPrefix(path, prefix+"/") {
		return path[len(prefix):]
	}
	if path == prefix {
		return "/"
	}
	return path
}

// stripHostPortForVhost 去掉 host 中的端口号，返回纯域名
func stripHostPortForVhost(host string) string {
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		if !strings.Contains(host, "[") {
			return host[:idx]
		}
	}
	return host
}