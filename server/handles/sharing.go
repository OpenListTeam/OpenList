package handles

import (
	"fmt"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/sharing"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/go-cache"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

func SharingGet(c *gin.Context, req *FsGetReq) {
	sid, path, _ := strings.Cut(strings.TrimPrefix(req.Path, "/"), "/")
	if sid == "" {
		common.ErrorStrResp(c, "invalid share id", 400)
		return
	}
	s, obj, err := sharing.Get(c.Request.Context(), sid, path, model.SharingListArgs{
		Refresh: false,
		Pwd:     req.Password,
	})
	if dealError(c, err) {
		return
	}
	_ = countAccess(c.ClientIP(), s)
	url := ""
	if !obj.IsDir() {
		fakePath := fmt.Sprintf("/%s/%s", sid, path)
		url = fmt.Sprintf("%s/sd%s", common.GetApiUrl(c), utils.EncodePath(fakePath, true))
		if s.Pwd != "" {
			url += "?pwd=" + s.Pwd
		}
	}
	thumb, _ := model.GetThumb(obj)
	common.SuccessResp(c, FsGetResp{
		ObjResp: ObjResp{
			Name:        obj.GetName(),
			Size:        obj.GetSize(),
			IsDir:       obj.IsDir(),
			Modified:    obj.ModTime(),
			Created:     obj.CreateTime(),
			HashInfoStr: obj.GetHash().String(),
			HashInfo:    obj.GetHash().Export(),
			Sign:        "",
			Type:        utils.GetFileType(obj.GetName()),
			Thumb:       thumb,
		},
		RawURL:   url,
		Readme:   s.Readme,
		Header:   s.Header,
		Provider: "unknown",
		Related:  nil,
	})
}

func SharingList(c *gin.Context, req *ListReq) {
	sid, path, _ := strings.Cut(strings.TrimPrefix(req.Path, "/"), "/")
	if sid == "" {
		common.ErrorStrResp(c, "invalid share id", 400)
		return
	}
	s, objs, err := sharing.List(c.Request.Context(), sid, path, model.SharingListArgs{
		Refresh: req.Refresh,
		Pwd:     req.Password,
	})
	if dealError(c, err) {
		return
	}
	_ = countAccess(c.ClientIP(), s)
	total, objs := pagination(objs, &req.PageReq)
	common.SuccessResp(c, FsListResp{
		Content: utils.MustSliceConvert(objs, func(obj model.Obj) ObjResp {
			thumb, _ := model.GetThumb(obj)
			return ObjResp{
				Name:        obj.GetName(),
				Size:        obj.GetSize(),
				IsDir:       obj.IsDir(),
				Modified:    obj.ModTime(),
				Created:     obj.CreateTime(),
				HashInfoStr: obj.GetHash().String(),
				HashInfo:    obj.GetHash().Export(),
				Sign:        "",
				Thumb:       thumb,
				Type:        utils.GetObjType(obj.GetName(), obj.IsDir()),
			}
		}),
		Total:    int64(total),
		Readme:   s.Readme,
		Header:   s.Header,
		Write:    false,
		Provider: "unknown",
	})
}

func SharingArchiveMeta(c *gin.Context, req *ArchiveMetaReq) {
	if !setting.GetBool(conf.ShareArchivePreview) {
		common.ErrorStrResp(c, "sharing archives previewing is not allowed", 403)
		return
	}
	sid, path, _ := strings.Cut(strings.TrimPrefix(req.Path, "/"), "/")
	if sid == "" {
		common.ErrorStrResp(c, "invalid share id", 400)
		return
	}
	archiveArgs := model.ArchiveArgs{
		LinkArgs: model.LinkArgs{
			Header: c.Request.Header,
			Type:   c.Query("type"),
		},
		Password: req.ArchivePass,
	}
	s, ret, err := sharing.ArchiveMeta(c.Request.Context(), sid, path, model.SharingArchiveMetaArgs{
		ArchiveMetaArgs: model.ArchiveMetaArgs{
			ArchiveArgs: archiveArgs,
			Refresh:     req.Refresh,
		},
		Pwd: req.Password,
	})
	if dealError(c, err) {
		return
	}
	_ = countAccess(c.ClientIP(), s)
	fakePath := fmt.Sprintf("/%s/%s", sid, path)
	url := fmt.Sprintf("%s/sad%s", common.GetApiUrl(c), utils.EncodePath(fakePath, true))
	if s.Pwd != "" {
		url += "?pwd=" + s.Pwd
	}
	common.SuccessResp(c, ArchiveMetaResp{
		Comment:     ret.GetComment(),
		IsEncrypted: ret.IsEncrypted(),
		Content:     toContentResp(ret.GetTree()),
		Sort:        ret.Sort,
		RawURL:      url,
		Sign:        "",
	})
}

func SharingArchiveList(c *gin.Context, req *ArchiveListReq) {
	if !setting.GetBool(conf.ShareArchivePreview) {
		common.ErrorStrResp(c, "sharing archives previewing is not allowed", 403)
		return
	}
	sid, path, _ := strings.Cut(strings.TrimPrefix(req.Path, "/"), "/")
	if sid == "" {
		common.ErrorStrResp(c, "invalid share id", 400)
		return
	}
	innerArgs := model.ArchiveInnerArgs{
		ArchiveArgs: model.ArchiveArgs{
			LinkArgs: model.LinkArgs{
				Header: c.Request.Header,
				Type:   c.Query("type"),
			},
			Password: req.ArchivePass,
		},
		InnerPath: utils.FixAndCleanPath(req.InnerPath),
	}
	s, objs, err := sharing.ArchiveList(c.Request.Context(), sid, path, model.SharingArchiveListArgs{
		ArchiveListArgs: model.ArchiveListArgs{
			ArchiveInnerArgs: innerArgs,
			Refresh:          req.Refresh,
		},
		Pwd: req.Password,
	})
	if dealError(c, err) {
		return
	}
	_ = countAccess(c.ClientIP(), s)
	total, objs := pagination(objs, &req.PageReq)
	ret, _ := utils.SliceConvert(objs, func(src model.Obj) (ObjResp, error) {
		return toObjsRespWithoutSignAndThumb(src), nil
	})
	common.SuccessResp(c, common.PageResp{
		Content: ret,
		Total:   int64(total),
	})
}

func SharingDown(c *gin.Context) {
	sid := c.Request.Context().Value(conf.SharingIDKey).(string)
	path := c.Request.Context().Value(conf.PathKey).(string)
	path = utils.FixAndCleanPath(path)
	pwd := c.Query("pwd")
	s, err := op.GetSharingById(sid)
	if err == nil {
		if !s.Valid() {
			err = errs.InvalidSharing
		} else if !s.Verify(pwd) {
			err = errs.WrongShareCode
		} else if len(s.Files) != 1 && path == "/" {
			err = errors.New("cannot get sharing root link")
		}
	}
	if dealErrorPage(c, err) {
		return
	}
	unwrapPath, err := op.GetSharingUnwrapPath(s, path)
	if err != nil {
		common.ErrorPage(c, errors.New("failed get sharing unwrap path"), 500)
		return
	}
	storage, actualPath, err := op.GetStorageAndActualPath(unwrapPath)
	if dealErrorPage(c, err) {
		return
	}
	if setting.GetBool(conf.ShareForceProxy) || common.ShouldProxy(storage, stdpath.Base(actualPath)) {
		if _, ok := c.GetQuery("d"); !ok {
			if url := common.GenerateDownProxyURL(storage.GetStorage(), unwrapPath); url != "" {
				c.Redirect(302, url)
				_ = countAccess(c.ClientIP(), s)
				return
			}
		}
		link, obj, err := op.Link(c.Request.Context(), storage, actualPath, model.LinkArgs{
			Header: c.Request.Header,
			Type:   c.Query("type"),
		})
		if err != nil {
			common.ErrorPage(c, errors.WithMessage(err, "failed get sharing link"), 500)
			return
		}
		_ = countAccess(c.ClientIP(), s)
		proxy(c, link, obj, storage.GetStorage().ProxyRange)
	} else {
		link, _, err := op.Link(c.Request.Context(), storage, actualPath, model.LinkArgs{
			IP:       c.ClientIP(),
			Header:   c.Request.Header,
			Type:     c.Query("type"),
			Redirect: true,
		})
		if err != nil {
			common.ErrorPage(c, errors.WithMessage(err, "failed get sharing link"), 500)
			return
		}
		_ = countAccess(c.ClientIP(), s)
		redirect(c, link)
	}
}

func SharingArchiveExtract(c *gin.Context) {
	if !setting.GetBool(conf.ShareArchivePreview) {
		common.ErrorPage(c, errors.New("sharing archives previewing is not allowed"), 403)
		return
	}
	sid := c.Request.Context().Value(conf.SharingIDKey).(string)
	path := c.Request.Context().Value(conf.PathKey).(string)
	path = utils.FixAndCleanPath(path)
	pwd := c.Query("pwd")
	innerPath := utils.FixAndCleanPath(c.Query("inner"))
	archivePass := c.Query("pass")
	s, err := op.GetSharingById(sid)
	if err == nil {
		if !s.Valid() {
			err = errs.InvalidSharing
		} else if !s.Verify(pwd) {
			err = errs.WrongShareCode
		} else if len(s.Files) != 1 && path == "/" {
			err = errors.New("cannot extract sharing root")
		}
	}
	if dealErrorPage(c, err) {
		return
	}
	unwrapPath, err := op.GetSharingUnwrapPath(s, path)
	if err != nil {
		common.ErrorPage(c, errors.New("failed get sharing unwrap path"), 500)
		return
	}
	storage, actualPath, err := op.GetStorageAndActualPath(unwrapPath)
	if dealErrorPage(c, err) {
		return
	}
	args := model.ArchiveInnerArgs{
		ArchiveArgs: model.ArchiveArgs{
			LinkArgs: model.LinkArgs{
				Header: c.Request.Header,
				Type:   c.Query("type"),
			},
			Password: archivePass,
		},
		InnerPath: innerPath,
	}
	if _, ok := storage.(driver.ArchiveReader); ok {
		if setting.GetBool(conf.ShareForceProxy) || common.ShouldProxy(storage, stdpath.Base(actualPath)) {
			link, obj, err := op.DriverExtract(c.Request.Context(), storage, actualPath, args)
			if dealErrorPage(c, err) {
				return
			}
			proxy(c, link, obj, storage.GetStorage().ProxyRange)
		} else {
			args.Redirect = true
			link, _, err := op.DriverExtract(c.Request.Context(), storage, actualPath, args)
			if dealErrorPage(c, err) {
				return
			}
			redirect(c, link)
		}
	} else {
		rc, size, err := op.InternalExtract(c.Request.Context(), storage, actualPath, args)
		if dealErrorPage(c, err) {
			return
		}
		fileName := stdpath.Base(innerPath)
		proxyInternalExtract(c, rc, size, fileName)
	}
}

func dealError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	} else if errors.Is(err, errs.SharingNotFound) {
		common.ErrorStrResp(c, "the share does not exist", 500)
	} else if errors.Is(err, errs.InvalidSharing) {
		common.ErrorStrResp(c, "the share has expired or is no longer valid", 500)
	} else if errors.Is(err, errs.WrongShareCode) {
		common.ErrorResp(c, err, 403)
	} else if errors.Is(err, errs.WrongArchivePassword) {
		common.ErrorResp(c, err, 202)
	} else {
		common.ErrorResp(c, err, 500)
	}
	return true
}

func dealErrorPage(c *gin.Context, err error) bool {
	if err == nil {
		return false
	} else if errors.Is(err, errs.SharingNotFound) {
		common.ErrorPage(c, errors.New("the share does not exist"), 500)
	} else if errors.Is(err, errs.InvalidSharing) {
		common.ErrorPage(c, errors.New("the share has expired or is no longer valid"), 500)
	} else if errors.Is(err, errs.WrongShareCode) {
		common.ErrorPage(c, err, 403)
	} else if errors.Is(err, errs.WrongArchivePassword) {
		common.ErrorPage(c, err, 202)
	} else {
		common.ErrorPage(c, err, 500)
	}
	return true
}

type SharingResp struct {
	*model.Sharing
	CreatorName string `json:"creator"`
	CreatorRole int    `json:"creator_role"`
}

func GetSharing(c *gin.Context) {
	sid := c.Query("id")
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	s, err := op.GetSharingById(sid)
	if err != nil || (!user.IsAdmin() && s.Creator.ID != user.ID) {
		common.ErrorStrResp(c, "sharing not found", 404)
		return
	}
	common.SuccessResp(c, SharingResp{
		Sharing:     s,
		CreatorName: s.Creator.Username,
		CreatorRole: s.Creator.Role,
	})
}

func ListSharings(c *gin.Context) {
	var req model.PageReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	req.Validate()
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	var sharings []model.Sharing
	var total int64
	var err error
	if user.IsAdmin() {
		sharings, total, err = op.GetSharings(req.Page, req.PerPage)
	} else {
		sharings, total, err = op.GetSharingsByCreatorId(user.ID, req.Page, req.PerPage)
	}
	if err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, common.PageResp{
		Content: utils.MustSliceConvert(sharings, func(s model.Sharing) SharingResp {
			return SharingResp{
				Sharing:     &s,
				CreatorName: s.Creator.Username,
				CreatorRole: s.Creator.Role,
			}
		}),
		Total: total,
	})
}

type UpdateSharingReq struct {
	Files       []string   `json:"files"`
	Expires     *time.Time `json:"expires"`
	Pwd         string     `json:"pwd"`
	MaxAccessed int        `json:"max_accessed"`
	Disabled    bool       `json:"disabled"`
	Remark      string     `json:"remark"`
	Readme      string     `json:"readme"`
	Header      string     `json:"header"`
	model.Sort
	CreatorName string `json:"creator"`
	Accessed    int    `json:"accessed"`
	ID          string `json:"id"`
}

func UpdateSharing(c *gin.Context) {
	var req UpdateSharingReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if len(req.Files) == 0 || (len(req.Files) == 1 && req.Files[0] == "") {
		common.ErrorStrResp(c, "must add at least 1 object", 400)
		return
	}
	var user *model.User
	var err error
	reqUser := c.Request.Context().Value(conf.UserKey).(*model.User)
	if reqUser.IsAdmin() && req.CreatorName != "" {
		user, err = op.GetUserByName(req.CreatorName)
		if err != nil {
			common.ErrorStrResp(c, "no such a user", 400)
			return
		}
	} else {
		user = reqUser
		if !user.CanShare() {
			common.ErrorStrResp(c, "permission denied", 403)
			return
		}
	}
	for i, s := range req.Files {
		s = utils.FixAndCleanPath(s)
		req.Files[i] = s
		if !reqUser.IsAdmin() && !strings.HasPrefix(s, user.BasePath) {
			common.ErrorStrResp(c, fmt.Sprintf("permission denied to share path [%s]", s), 500)
			return
		}
	}
	s, err := op.GetSharingById(req.ID)
	if err != nil || (!reqUser.IsAdmin() && s.CreatorId != user.ID) {
		common.ErrorStrResp(c, "sharing not found", 404)
		return
	}
	if reqUser.IsAdmin() && req.CreatorName == "" {
		user = s.Creator
	}
	s.Files = req.Files
	s.Expires = req.Expires
	s.Pwd = req.Pwd
	s.Accessed = req.Accessed
	s.MaxAccessed = req.MaxAccessed
	s.Disabled = req.Disabled
	s.Sort = req.Sort
	s.Header = req.Header
	s.Readme = req.Readme
	s.Remark = req.Remark
	s.Creator = user
	if err = op.UpdateSharing(s); err != nil {
		common.ErrorResp(c, err, 500)
	} else {
		common.SuccessResp(c, SharingResp{
			Sharing:     s,
			CreatorName: s.Creator.Username,
			CreatorRole: s.Creator.Role,
		})
	}
}

func CreateSharing(c *gin.Context) {
	var req UpdateSharingReq
	var err error
	if err = c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if len(req.Files) == 0 || (len(req.Files) == 1 && req.Files[0] == "") {
		common.ErrorStrResp(c, "must add at least 1 object", 400)
		return
	}
	var user *model.User
	reqUser := c.Request.Context().Value(conf.UserKey).(*model.User)
	if reqUser.IsAdmin() && req.CreatorName != "" {
		user, err = op.GetUserByName(req.CreatorName)
		if err != nil {
			common.ErrorStrResp(c, "no such a user", 400)
			return
		}
	} else {
		user = reqUser
		if !user.CanShare() || (!user.IsAdmin() && req.ID != "") {
			common.ErrorStrResp(c, "permission denied", 403)
			return
		}
	}
	for i, s := range req.Files {
		s = utils.FixAndCleanPath(s)
		req.Files[i] = s
		if !reqUser.IsAdmin() && !strings.HasPrefix(s, user.BasePath) {
			common.ErrorStrResp(c, fmt.Sprintf("permission denied to share path [%s]", s), 500)
			return
		}
	}
	s := &model.Sharing{
		SharingDB: &model.SharingDB{
			ID:          req.ID,
			Expires:     req.Expires,
			Pwd:         req.Pwd,
			Accessed:    req.Accessed,
			MaxAccessed: req.MaxAccessed,
			Disabled:    req.Disabled,
			Sort:        req.Sort,
			Remark:      req.Remark,
			Readme:      req.Readme,
			Header:      req.Header,
		},
		Files:   req.Files,
		Creator: user,
	}
	var id string
	if id, err = op.CreateSharing(s); err != nil {
		common.ErrorResp(c, err, 500)
	} else {
		s.ID = id
		common.SuccessResp(c, SharingResp{
			Sharing:     s,
			CreatorName: s.Creator.Username,
			CreatorRole: s.Creator.Role,
		})
	}
}

func DeleteSharing(c *gin.Context) {
	sid := c.Query("id")
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	s, err := op.GetSharingById(sid)
	if err != nil || (!user.IsAdmin() && s.CreatorId != user.ID) {
		common.ErrorResp(c, err, 404)
		return
	}
	if err = op.DeleteSharing(sid); err != nil {
		common.ErrorResp(c, err, 500)
	} else {
		common.SuccessResp(c)
	}
}

func SetEnableSharing(disable bool) func(ctx *gin.Context) {
	return func(c *gin.Context) {
		sid := c.Query("id")
		user := c.Request.Context().Value(conf.UserKey).(*model.User)
		s, err := op.GetSharingById(sid)
		if err != nil || (!user.IsAdmin() && s.CreatorId != user.ID) {
			common.ErrorStrResp(c, "sharing not found", 404)
			return
		}
		s.Disabled = disable
		if err = op.UpdateSharing(s, true); err != nil {
			common.ErrorResp(c, err, 500)
		} else {
			common.SuccessResp(c)
		}
	}
}

var (
	AccessCache      = cache.NewMemCache[interface{}]()
	AccessCountDelay = 30 * time.Minute
)

func countAccess(ip string, s *model.Sharing) error {
	key := fmt.Sprintf("%s:%s", s.ID, ip)
	_, ok := AccessCache.Get(key)
	if !ok {
		AccessCache.Set(key, struct{}{}, cache.WithEx[interface{}](AccessCountDelay))
		s.Accessed += 1
		return op.UpdateSharing(s, true)
	}
	return nil
}
