package handles

import (
	"fmt"
	"io"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/archive/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type ArchiveMetaReq struct {
	Path        string `json:"path" form:"path"`
	Password    string `json:"password" form:"password"`
	Refresh     bool   `json:"refresh" form:"refresh"`
	ArchivePass string `json:"archive_pass" form:"archive_pass"`
}

type ArchiveMetaResp struct {
	Comment     string               `json:"comment"`
	IsEncrypted bool                 `json:"encrypted"`
	Content     []ArchiveContentResp `json:"content"`
	Sort        *model.Sort          `json:"sort,omitempty"`
	RawURL      string               `json:"raw_url"`
	Sign        string               `json:"sign"`
}

type ArchiveContentResp struct {
	ObjResp
	Children []ArchiveContentResp `json:"children"`
}

func toObjsRespWithoutSignAndThumb(obj model.Obj) ObjResp {
	return ObjResp{
		Name:        obj.GetName(),
		Size:        obj.GetSize(),
		IsDir:       obj.IsDir(),
		Modified:    obj.ModTime(),
		Created:     obj.CreateTime(),
		HashInfoStr: obj.GetHash().String(),
		HashInfo:    obj.GetHash().Export(),
		Sign:        "",
		Thumb:       "",
		Type:        utils.GetObjType(obj.GetName(), obj.IsDir()),
	}
}

func toContentResp(objs []model.ObjTree) []ArchiveContentResp {
	if objs == nil {
		return nil
	}
	ret, _ := utils.SliceConvert(objs, func(src model.ObjTree) (ArchiveContentResp, error) {
		return ArchiveContentResp{
			ObjResp:  toObjsRespWithoutSignAndThumb(src),
			Children: toContentResp(src.GetChildren()),
		}, nil
	})
	return ret
}

func FsArchiveMetaSplit(c *gin.Context) {
	var req ArchiveMetaReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if strings.HasPrefix(req.Path, "/@s") {
		req.Path = strings.TrimPrefix(req.Path, "/@s")
		SharingArchiveMeta(c, &req)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if user.IsGuest() && user.Disabled {
		common.ErrorStrResp(c, "Guest user is disabled, login please", 401)
		return
	}
	FsArchiveMeta(c, &req, user)
}

func FsArchiveMeta(c *gin.Context, req *ArchiveMetaReq, user *model.User) {
	if !user.CanReadArchives() {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
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
	archiveArgs := model.ArchiveArgs{
		LinkArgs: model.LinkArgs{
			Header: c.Request.Header,
			Type:   c.Query("type"),
		},
		Password: req.ArchivePass,
	}
	ret, err := fs.ArchiveMeta(c.Request.Context(), reqPath, model.ArchiveMetaArgs{
		ArchiveArgs: archiveArgs,
		Refresh:     req.Refresh,
	})
	if err != nil {
		if errors.Is(err, errs.WrongArchivePassword) {
			common.ErrorResp(c, err, 202)
		} else {
			common.ErrorResp(c, err, 500)
		}
		return
	}
	s := ""
	if isEncrypt(meta, reqPath) || setting.GetBool(conf.SignAll) {
		s = sign.SignArchive(reqPath)
	}
	api := "/ae"
	if ret.DriverProviding {
		api = "/ad"
	}
	common.SuccessResp(c, ArchiveMetaResp{
		Comment:     ret.GetComment(),
		IsEncrypted: ret.IsEncrypted(),
		Content:     toContentResp(ret.GetTree()),
		Sort:        ret.Sort,
		RawURL:      fmt.Sprintf("%s%s%s", common.GetApiUrl(c), api, utils.EncodePath(reqPath, true)),
		Sign:        s,
	})
}

type ArchiveListReq struct {
	ArchiveMetaReq
	model.PageReq
	InnerPath string `json:"inner_path" form:"inner_path"`
}

func FsArchiveListSplit(c *gin.Context) {
	var req ArchiveListReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	req.Validate()
	if strings.HasPrefix(req.Path, "/@s") {
		req.Path = strings.TrimPrefix(req.Path, "/@s")
		SharingArchiveList(c, &req)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if user.IsGuest() && user.Disabled {
		common.ErrorStrResp(c, "Guest user is disabled, login please", 401)
		return
	}
	FsArchiveList(c, &req, user)
}

func FsArchiveList(c *gin.Context, req *ArchiveListReq, user *model.User) {
	if !user.CanReadArchives() {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
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
	objs, err := fs.ArchiveList(c.Request.Context(), reqPath, model.ArchiveListArgs{
		ArchiveInnerArgs: model.ArchiveInnerArgs{
			ArchiveArgs: model.ArchiveArgs{
				LinkArgs: model.LinkArgs{
					Header: c.Request.Header,
					Type:   c.Query("type"),
				},
				Password: req.ArchivePass,
			},
			InnerPath: utils.FixAndCleanPath(req.InnerPath),
		},
		Refresh: req.Refresh,
	})
	if err != nil {
		if errors.Is(err, errs.WrongArchivePassword) {
			common.ErrorResp(c, err, 202)
		} else {
			common.ErrorResp(c, err, 500)
		}
		return
	}
	total, objs := pagination(objs, &req.PageReq)
	ret, _ := utils.SliceConvert(objs, func(src model.Obj) (ObjResp, error) {
		return toObjsRespWithoutSignAndThumb(src), nil
	})
	common.SuccessResp(c, common.PageResp{
		Content: ret,
		Total:   int64(total),
	})
}

type ArchiveDecompressReq struct {
	SrcDir        string   `json:"src_dir" form:"src_dir"`
	DstDir        string   `json:"dst_dir" form:"dst_dir"`
	Names         []string `json:"name" form:"name"`
	ArchivePass   string   `json:"archive_pass" form:"archive_pass"`
	InnerPath     string   `json:"inner_path" form:"inner_path"`
	CacheFull     bool     `json:"cache_full" form:"cache_full"`
	PutIntoNewDir bool     `json:"put_into_new_dir" form:"put_into_new_dir"`
	Overwrite     bool     `json:"overwrite" form:"overwrite"`
}

func FsArchiveDecompress(c *gin.Context) {
	var req ArchiveDecompressReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if !user.CanDecompress() {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	srcPaths := make([]string, 0, len(req.Names))
	for _, name := range req.Names {
		srcPath, err := user.JoinPath(stdpath.Join(req.SrcDir, name))
		if err != nil {
			common.ErrorResp(c, err, 403)
			return
		}
		srcPaths = append(srcPaths, srcPath)
	}
	dstDir, err := user.JoinPath(req.DstDir)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	tasks := make([]task.TaskExtensionInfo, 0, len(srcPaths))
	for _, srcPath := range srcPaths {
		t, e := fs.ArchiveDecompress(c.Request.Context(), srcPath, dstDir, model.ArchiveDecompressArgs{
			ArchiveInnerArgs: model.ArchiveInnerArgs{
				ArchiveArgs: model.ArchiveArgs{
					LinkArgs: model.LinkArgs{
						Header: c.Request.Header,
						Type:   c.Query("type"),
					},
					Password: req.ArchivePass,
				},
				InnerPath: utils.FixAndCleanPath(req.InnerPath),
			},
			CacheFull:     req.CacheFull,
			PutIntoNewDir: req.PutIntoNewDir,
			Overwrite:     req.Overwrite,
		})
		if e != nil {
			if errors.Is(e, errs.WrongArchivePassword) {
				common.ErrorResp(c, e, 202)
			} else {
				common.ErrorResp(c, e, 500)
			}
			return
		}
		if t != nil {
			tasks = append(tasks, t)
		}
	}
	common.SuccessResp(c, gin.H{
		"task": getTaskInfos(tasks),
	})
}

func ArchiveDown(c *gin.Context) {
	archiveRawPath := c.Request.Context().Value(conf.PathKey).(string)
	innerPath := utils.FixAndCleanPath(c.Query("inner"))
	password := c.Query("pass")
	filename := stdpath.Base(innerPath)
	storage, err := fs.GetStorage(archiveRawPath, &fs.GetStoragesArgs{})
	if err != nil {
		common.ErrorPage(c, err, 500)
		return
	}
	if common.ShouldProxy(storage, filename) {
		ArchiveProxy(c)
		return
	} else {
		link, _, err := fs.ArchiveDriverExtract(c.Request.Context(), archiveRawPath, model.ArchiveInnerArgs{
			ArchiveArgs: model.ArchiveArgs{
				LinkArgs: model.LinkArgs{
					IP:       c.ClientIP(),
					Header:   c.Request.Header,
					Type:     c.Query("type"),
					Redirect: true,
				},
				Password: password,
			},
			InnerPath: innerPath,
		})
		if err != nil {
			common.ErrorPage(c, err, 500)
			return
		}
		redirect(c, link)
	}
}

func ArchiveProxy(c *gin.Context) {
	archiveRawPath := c.Request.Context().Value(conf.PathKey).(string)
	innerPath := utils.FixAndCleanPath(c.Query("inner"))
	password := c.Query("pass")
	filename := stdpath.Base(innerPath)
	storage, err := fs.GetStorage(archiveRawPath, &fs.GetStoragesArgs{})
	if err != nil {
		common.ErrorPage(c, err, 500)
		return
	}
	if canProxy(storage, filename) {
		// TODO: Support external download proxy URL
		link, file, err := fs.ArchiveDriverExtract(c.Request.Context(), archiveRawPath, model.ArchiveInnerArgs{
			ArchiveArgs: model.ArchiveArgs{
				LinkArgs: model.LinkArgs{
					Header: c.Request.Header,
					Type:   c.Query("type"),
				},
				Password: password,
			},
			InnerPath: innerPath,
		})
		if err != nil {
			common.ErrorPage(c, err, 500)
			return
		}
		proxy(c, link, file, storage.GetStorage().ProxyRange)
	} else {
		common.ErrorPage(c, errors.New("proxy not allowed"), 403)
		return
	}
}

func proxyInternalExtract(c *gin.Context, rc io.ReadCloser, size int64, fileName string) {
	defer func() {
		if err := rc.Close(); err != nil {
			log.Errorf("failed to close file streamer, %v", err)
		}
	}()
	headers := map[string]string{
		"Referrer-Policy": "no-referrer",
		"Cache-Control":   "max-age=0, no-cache, no-store, must-revalidate",
	}
	headers["Content-Disposition"] = utils.GenerateContentDisposition(fileName)
	contentType := c.Request.Header.Get("Content-Type")
	if contentType == "" {
		contentType = utils.GetMimeType(fileName)
	}
	c.DataFromReader(200, size, contentType, rc, headers)
}

func ArchiveInternalExtract(c *gin.Context) {
	archiveRawPath := c.Request.Context().Value(conf.PathKey).(string)
	innerPath := utils.FixAndCleanPath(c.Query("inner"))
	password := c.Query("pass")
	rc, size, err := fs.ArchiveInternalExtract(c.Request.Context(), archiveRawPath, model.ArchiveInnerArgs{
		ArchiveArgs: model.ArchiveArgs{
			LinkArgs: model.LinkArgs{
				Header: c.Request.Header,
				Type:   c.Query("type"),
			},
			Password: password,
		},
		InnerPath: innerPath,
	})
	if err != nil {
		common.ErrorPage(c, err, 500)
		return
	}
	fileName := stdpath.Base(innerPath)
	proxyInternalExtract(c, rc, size, fileName)
}

func ArchiveExtensions(c *gin.Context) {
	var ext []string
	for key := range tool.Tools {
		ext = append(ext, key)
	}
	common.SuccessResp(c, ext)
}
