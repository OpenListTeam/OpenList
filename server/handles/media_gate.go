package handles

import (
	"crypto/subtle"
	"fmt"
	"net/url"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/media"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

// MediaEnabled 媒体库总开关是否开启
func MediaEnabled() bool {
	return setting.GetBool(conf.MediaEnabled)
}

// MediaEmbyEnabled Emby 兼容 API 是否可用（依赖总开关）
func MediaEmbyEnabled() bool {
	return MediaEnabled() && setting.GetBool(conf.MediaEmbyEnabled)
}

// MediaSubsonicEnabled OpenSubsonic API 是否可用（依赖总开关）
func MediaSubsonicEnabled() bool {
	return MediaEnabled() && setting.GetBool(conf.MediaSubsonicEnabled)
}

// RequireMediaEnabled 媒体库关闭时统一返回 404，使其表现为"功能不存在"
func RequireMediaEnabled(c *gin.Context) {
	if !MediaEnabled() {
		c.AbortWithStatus(404)
	}
}

// RequireMediaProtocol 校验指定协议开关
func RequireMediaProtocol(enabled func() bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !enabled() {
			c.AbortWithStatus(404)
		}
	}
}

// mediaUser 返回当前请求的登录用户，未登录时为 nil
func mediaUser(c *gin.Context) *model.User {
	user, _ := c.Request.Context().Value(conf.UserKey).(*model.User)
	return user
}

// CanUserAccessPath 判断用户是否有权访问指定 VFS 路径
// 规则与文件浏览保持一致：base_path 前缀 + 元信息(meta) 读权限 + 隐藏文件规则。
//
// user 为 nil 一律拒绝（fail-closed）：所有调用点都应经过 Auth / embyAuth 拿到用户，
// 缺失用户说明鉴权链路出了问题，不能默认放行；被禁用的访客同样拒绝。
func CanUserAccessPath(user *model.User, reqPath string) bool {
	if user == nil || (user.IsGuest() && user.Disabled) {
		return false
	}
	reqPath = utils.FixAndCleanPath(reqPath)
	base := utils.FixAndCleanPath(user.BasePath)
	if base == "" {
		base = "/"
	}
	if base != "/" && !utils.IsSubPath(base, reqPath) && !utils.PathEqual(base, reqPath) {
		return false
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return false
	}
	// media 播放链路无法携带目录密码，因此受密码保护的目录视为不可访问
	return common.CanAccess(user, meta, reqPath, "")
}

// userPathPrefix 返回用于 SQL 前缀过滤的用户目录，全盘用户返回空串
func userPathPrefix(user *model.User) string {
	if user == nil {
		return ""
	}
	base := utils.FixAndCleanPath(user.BasePath)
	if base == "" || base == "/" {
		return ""
	}
	return base
}

// mediaFolderOf 返回条目所在目录
func mediaFolderOf(item *model.MediaItem) string {
	if item == nil {
		return "/"
	}
	folder := strings.TrimSuffix(item.FolderPath, "/")
	if folder == "" {
		folder = "/"
	}
	return folder
}

// MediaItemFilePath 拼接媒体条目对应的实际文件路径
func MediaItemFilePath(item *model.MediaItem) string {
	if item == nil {
		return ""
	}
	folder := strings.TrimSuffix(item.FolderPath, "/")
	if folder == "" {
		folder = "/"
	}
	return stdpath.Join(folder, item.FileName)
}

// MediaEpisodeFilePath 拼接文件夹型条目的单集文件路径
func MediaEpisodeFilePath(item *model.MediaItem, ep *model.EpisodeInfo) string {
	if item == nil || ep == nil {
		return ""
	}
	return stdpath.Join(MediaItemFilePath(item), ep.FileName)
}

// CanUserAccessItem 判断用户是否可访问某个媒体条目（含所属媒体类型开关）
func CanUserAccessItem(user *model.User, item *model.MediaItem) bool {
	if item == nil || item.Hidden {
		return false
	}
	if !MediaTypeEnabled(item.MediaType) {
		return false
	}
	return CanUserAccessPath(user, MediaItemFilePath(item))
}

// MediaTypeEnabled 判断某媒体类型是否启用扫描
func MediaTypeEnabled(mediaType model.MediaType) bool {
	cfg, err := db.GetMediaConfig(mediaType)
	if err != nil || cfg == nil {
		return false
	}
	return cfg.Enabled
}

// MediaTypeEnabledList 返回已启用的媒体类型
func MediaTypeEnabledList() []model.MediaType {
	all := []model.MediaType{model.MediaTypeVideo, model.MediaTypeMusic, model.MediaTypeImage, model.MediaTypeBook}
	enabled := make([]model.MediaType, 0, len(all))
	for _, mt := range all {
		if MediaTypeEnabled(mt) {
			enabled = append(enabled, mt)
		}
	}
	return enabled
}

// LookUpAppKeyUser 按明文应用密钥查找对应用户与密钥记录。
// Emby 与 Subsonic 的所有密钥认证都走这里，避免两套实现产生行为差异。
func LookUpAppKeyUser(key string) (*model.User, *model.MediaAppKey, bool) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "olmp_") {
		return nil, nil, false
	}
	keys, err := db.ListMediaAppKeysByPrefix(media.AppKeyPrefix(key))
	if err != nil {
		return nil, nil, false
	}
	for i := range keys {
		plain, err := media.DecryptAppKey(keys[i].Secret)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(plain), []byte(key)) != 1 {
			continue
		}
		user, err := op.GetUserById(keys[i].UserID)
		if err != nil || user.Disabled || user.IsGuest() {
			return nil, nil, false
		}
		db.TouchMediaAppKey(keys[i].ID)
		return user, &keys[i], true
	}
	return nil, nil, false
}

// SignedStreamURL 构造带签名的 /d 直链（绝对地址，供 Emby/Subsonic/STRM 使用）
func SignedStreamURL(c *gin.Context, rawPath string) string {
	return SignedStreamURLWithBase(common.GetApiUrl(c.Request.Context()), rawPath)
}

// SignedStreamURLWithBase 使用指定站点前缀构造签名直链
func SignedStreamURLWithBase(apiBase, rawPath string) string {
	apiBase = strings.TrimSuffix(apiBase, "/")
	p := rawPath
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return fmt.Sprintf("%s/d%s?sign=%s", apiBase, utils.EncodePath(p), url.QueryEscape(sign.Sign(rawPath)))
}

// SignedProxyURL 构造经服务端代理的签名链接（/p）
func SignedProxyURL(c *gin.Context, rawPath string) string {
	apiBase := strings.TrimSuffix(common.GetApiUrl(c.Request.Context()), "/")
	p := rawPath
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return fmt.Sprintf("%s/p%s?sign=%s", apiBase, utils.EncodePath(p), url.QueryEscape(sign.Sign(rawPath)))
}

// MediaStreamURL 返回媒体条目（或其中一集）的签名直链
func MediaStreamURL(c *gin.Context, item *model.MediaItem, ep *model.EpisodeInfo) string {
	if ep != nil {
		return SignedStreamURL(c, MediaEpisodeFilePath(item, ep))
	}
	return SignedStreamURL(c, MediaItemFilePath(item))
}
