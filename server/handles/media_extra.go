package handles

import (
	"encoding/json"
	"fmt"
	"net/url"
	stdpath "path"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/media"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// MediaStatus 媒体库状态（前端据此决定是否渲染媒体库入口/侧栏）
type MediaStatus struct {
	Enabled         bool     `json:"enabled"`
	Types           []string `json:"types"`
	EmbyEnabled     bool     `json:"emby_enabled"`
	SubsonicEnabled bool     `json:"subsonic_enabled"`
	EmbyBaseURL     string   `json:"emby_base_url"`
	SubsonicBaseURL string   `json:"subsonic_base_url"`
	StrmPath        string   `json:"strm_path"`
	StrmOnScan      bool     `json:"strm_on_scan"`
	StrmSameDir     bool     `json:"strm_same_dir"`
	ThumbPath       string   `json:"thumb_path"`
	Version         string   `json:"version"`
	ProxyCover      bool     `json:"proxy_cover"`
}

// PublicMediaStatus 公开的媒体库状态
func PublicMediaStatus(c *gin.Context) {
	// Types 必须是非 nil 切片：前端会直接调用 .includes()，nil 序列化成 null 会抛异常
	status := MediaStatus{Enabled: MediaEnabled(), Types: []string{}}
	apiBase := common.GetApiUrl(c.Request.Context())
	status.StrmPath = media.DefaultStrmOptions(apiBase).OutputPath
	status.StrmOnScan = setting.GetBool(conf.MediaStrmOnScan)
	status.StrmSameDir = setting.GetBool(conf.MediaStrmSameDir)
	status.ThumbPath = setting.GetStr(conf.MediaThumbnailPath, "/imgs")
	status.Version = conf.Version
	status.ProxyCover = setting.GetBool(conf.MediaProxyCover)
	if !status.Enabled {
		common.SuccessResp(c, status)
		return
	}
	for _, mt := range MediaTypeEnabledList() {
		status.Types = append(status.Types, string(mt))
	}
	status.EmbyEnabled = MediaEmbyEnabled()
	status.SubsonicEnabled = MediaSubsonicEnabled()
	status.EmbyBaseURL = strings.TrimSuffix(apiBase, "/") + "/@emby"
	status.SubsonicBaseURL = strings.TrimSuffix(apiBase, "/") + "/@rest"
	common.SuccessResp(c, status)
}

// ==================== 应用密钥 ====================

// ListMediaAppKeys 列出当前管理员的媒体应用密钥
func ListMediaAppKeys(c *gin.Context) {
	user := mediaUser(c)
	if user == nil {
		common.ErrorStrResp(c, "未登录", 401)
		return
	}
	keys, err := db.ListMediaAppKeys(user.ID)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, keys)
}

// CreateMediaAppKey 创建应用密钥（明文只在创建时返回一次）
func CreateMediaAppKey(c *gin.Context) {
	user := mediaUser(c)
	if user == nil {
		common.ErrorStrResp(c, "未登录", 401)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = c.ShouldBindJSON(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Media Key"
	}
	plain, err := media.NewAppKeyPlain()
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	secret, err := media.EncryptAppKey(plain)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	key := &model.MediaAppKey{
		UserID: user.ID,
		Name:   name,
		Prefix: media.AppKeyPrefix(plain),
		Secret: secret,
	}
	if err := db.CreateMediaAppKey(key); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, gin.H{
		"id":     key.ID,
		"name":   key.Name,
		"prefix": key.Prefix,
		"key":    plain,
	})
}

// DeleteMediaAppKey 删除应用密钥
func DeleteMediaAppKey(c *gin.Context) {
	user := mediaUser(c)
	if user == nil {
		common.ErrorStrResp(c, "未登录", 401)
		return
	}
	id, err := strconv.ParseUint(c.Query("id"), 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "无效的ID", 400)
		return
	}
	if err := db.DeleteMediaAppKey(user.ID, uint(id)); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, nil)
}

// ==================== STRM ====================

// GenerateMediaStrm 手动批量生成 STRM
func GenerateMediaStrm(c *gin.Context) {
	var req struct {
		MediaType  model.MediaType `json:"media_type"`
		ScanPathID uint            `json:"scan_path_id"`
	}
	_ = c.ShouldBindJSON(&req)
	apiRoot := common.GetApiUrl(c.Request.Context())
	media.SetAPIRoot(apiRoot)
	opts := media.DefaultStrmOptions(apiRoot)
	if !opts.SameDir && opts.OutputPath == "" {
		common.ErrorStrResp(c, "未配置 STRM 输出目录", 400)
		return
	}
	n, err := media.GenerateStrmForType(c.Request.Context(), req.MediaType, req.ScanPathID, opts)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	outPath := opts.OutputPath
	if opts.SameDir {
		outPath = "与媒体文件同目录"
	}
	common.SuccessResp(c, gin.H{"written": n, "path": outPath})
}

// GetMediaStreamURL 返回条目（或其中一集）的签名直链，供本地播放器/外链使用
func GetMediaStreamURL(c *gin.Context) {
	// 直接按路径取签名链接（用于选集等场景）
	if rawPath := c.Query("path"); rawPath != "" {
		if !CanUserAccessPath(mediaUser(c), rawPath) {
			common.ErrorStrResp(c, "资源不存在", 404)
			return
		}
		common.SuccessResp(c, gin.H{
			"url":     SignedStreamURL(c, rawPath),
			"path":    rawPath,
			"name":    stdpath.Base(rawPath),
			"proxied": SignedProxyURL(c, rawPath),
		})
		return
	}
	id, err := strconv.ParseUint(c.Query("id"), 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "无效的ID", 400)
		return
	}
	item, err := db.GetMediaItemByID(uint(id))
	if err != nil || !CanUserAccessItem(mediaUser(c), item) {
		common.ErrorStrResp(c, "资源不存在", 404)
		return
	}
	episodeIndex, _ := strconv.Atoi(c.DefaultQuery("episode", "0"))
	rawPath := MediaItemFilePath(item)
	if episodeIndex > 0 {
		var eps []model.EpisodeInfo
		if err := json.Unmarshal([]byte(item.Episodes), &eps); err == nil {
			for i := range eps {
				if eps[i].Index == episodeIndex || i+1 == episodeIndex {
					rawPath = MediaEpisodeFilePath(item, &eps[i])
					break
				}
			}
		}
	}
	// 选集是目录里的另一个文件，必须对最终文件再校验一次：
	// 目录可见不代表其中每个文件都可见（例如 meta 隐藏规则单独禁止某个文件），
	// 而签名之后的下载链路只验签名，不会重新执行用户的隐藏规则。
	if !CanUserAccessPath(mediaUser(c), rawPath) {
		common.ErrorStrResp(c, "资源不存在", 404)
		return
	}
	common.SuccessResp(c, gin.H{
		"url":     SignedStreamURL(c, rawPath),
		"path":    rawPath,
		"name":    stdpath.Base(rawPath),
		"proxied": SignedProxyURL(c, rawPath),
	})
}

// GetMediaStrm 动态生成单个条目的 .strm 内容（详情页下载）
func GetMediaStrm(c *gin.Context) {
	id, err := strconv.ParseUint(c.Query("id"), 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "无效的ID", 400)
		return
	}
	item, err := db.GetMediaItemByID(uint(id))
	if err != nil {
		common.ErrorStrResp(c, "资源不存在", 404)
		return
	}
	if !CanUserAccessItem(mediaUser(c), item) {
		common.ErrorStrResp(c, "资源不存在", 404)
		return
	}
	episodeIndex, _ := strconv.Atoi(c.DefaultQuery("episode", "0"))

	var rawPath string
	var filename string
	if episodeIndex > 0 {
		var eps []model.EpisodeInfo
		if err := json.Unmarshal([]byte(item.Episodes), &eps); err == nil {
			for i := range eps {
				if eps[i].Index == episodeIndex || i+1 == episodeIndex {
					rawPath = MediaEpisodeFilePath(item, &eps[i])
					filename = fmt.Sprintf("%s S%02dE%02d.strm", item.ScrapedName, item.Season, episodeIndex)
					break
				}
			}
		}
	}
	if rawPath == "" {
		rawPath = MediaItemFilePath(item)
		if item.IsFolder {
			filename = item.ScrapedName + ".strm"
		} else {
			filename = strings.TrimSuffix(item.FileName, stdpath.Ext(item.FileName)) + ".strm"
		}
	}
	// 与 GetMediaStreamURL 同理：选集解析出来的是另一个文件，需要按最终路径再校验一次
	if !CanUserAccessPath(mediaUser(c), rawPath) {
		common.ErrorStrResp(c, "资源不存在", 404)
		return
	}
	if filename == "" {
		filename = "media.strm"
	}

	apiRoot := common.GetApiUrl(c.Request.Context())
	opts := media.DefaultStrmOptions(apiRoot)
	content := media.RenderStrmContent(opts.Template, apiRoot, rawPath)
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(filename)))
	c.String(200, content)
}
