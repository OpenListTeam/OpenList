package handles

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"net"
	"net/http"
	stdpath "path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/media"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// ==================== 数据结构（Emby 使用 PascalCase 字段名） ====================

type embyNameID struct {
	Name string
	// Id 在官方 DTO 中是 Guid?，空字符串必须省略
	Id string `json:",omitempty"`
}

type embyUserData struct {
	PlaybackPositionTicks int64
	PlayCount             int
	IsFavorite            bool
	Played                bool
	UnplayedItemCount     int
	PlayedPercentage      float64
	Rating                float64 `json:",omitempty"`
	Likes                 bool
	// LastPlayedDate 是 DateTime?，空字符串必须省略
	LastPlayedDate string `json:",omitempty"`
}

// embyMediaSource 对应官方 MediaSourceInfo。
// 客户端按 SupportsDirectPlay -> SupportsDirectStream -> TranscodingUrl 的顺序判定播放方式，
// 同时读取 MediaStreams（视频/音频轨）和 RequiresOpening 等字段，缺失会被判定为"不可播放"。
type embyMediaSource struct {
	Protocol                string
	Id                      string
	Path                    string `json:",omitempty"`
	Type                    string
	Container               string `json:",omitempty"`
	Name                    string `json:",omitempty"`
	Size                    int64  `json:",omitempty"`
	IsRemote                bool
	ETag                    string `json:",omitempty"`
	RunTimeTicks            int64  `json:",omitempty"`
	ReadAtNativeFramerate   bool
	IgnoreDts               bool
	IgnoreIndex             bool
	GenPtsInput             bool
	SupportsTranscoding     bool
	SupportsDirectStream    bool
	SupportsDirectPlay      bool
	IsInfiniteStream        bool
	RequiresOpening         bool
	RequiresClosing         bool
	RequiresLooping         bool
	SupportsProbing         bool
	VideoType               string `json:",omitempty"`
	MediaStreams            []embyMediaStream
	MediaAttachments        []any
	Formats                 []string
	Bitrate                 int `json:",omitempty"`
	RequiredHttpHeaders     map[string]string
	DirectStreamUrl            string `json:",omitempty"`
	AddApiKeyToDirectStreamUrl bool
	DefaultAudioStreamIndex    int  `json:",omitempty"`
	// DefaultSubtitleStreamIndex 指向歌词字幕轨：客户端歌词面板只认
	// Type=="Subtitle" 且 Index 等于该值的流，缺失就不请求歌词
	DefaultSubtitleStreamIndex *int   `json:",omitempty"`
	ItemId                     string `json:",omitempty"`
}

// embyMediaStream 对应官方 MediaStream，只填写客户端判定播放所需的最小字段。
type embyMediaStream struct {
	Codec                  string `json:",omitempty"`
	Type                   string
	Index                  int
	IsDefault              bool
	IsForced               bool
	IsExternal             bool
	IsInterlaced           bool
	IsTextSubtitleStream   bool
	SupportsExternalStream bool
	Channels               int    `json:",omitempty"`
	SampleRate             int    `json:",omitempty"`
	BitRate                int    `json:",omitempty"`
	DisplayTitle           string `json:",omitempty"`
	Language               string `json:",omitempty"`
	DeliveryMethod         string `json:",omitempty"`
	DeliveryUrl            string `json:",omitempty"`
}

// embyItem 字段名与类型严格对应 Emby/Jellyfin 的 BaseItemDto：
// Id/ParentId/SeriesId/SeasonId/AlbumId 是 Guid，DateCreated/PremiereDate/EndDate 是 DateTime?。
// 空值必须省略（omitempty），否则客户端会把 "" 当作 Guid/DateTime 解析并抛异常。
type embyItem struct {
	Name                         string          `json:",omitempty"`
	ServerId                     string          `json:",omitempty"`
	Id                           string          `json:",omitempty"`
	DateCreated                  string          `json:",omitempty"`
	Container                    string          `json:",omitempty"`
	SortName                     string          `json:",omitempty"`
	PremiereDate                 string          `json:",omitempty"`
	Overview                     string          `json:",omitempty"`
	ProductionYear               int             `json:",omitempty"`
	CommunityRating              float32         `json:",omitempty"`
	OfficialRating               string          `json:",omitempty"`
	RunTimeTicks                 int64           `json:",omitempty"`
	IndexNumber                  int             `json:",omitempty"`
	ParentIndexNumber            int             `json:",omitempty"`
	Type                         string          `json:",omitempty"`
	MediaType                    string          `json:",omitempty"`
	IsFolder                     bool
	ParentId                     string          `json:",omitempty"`
	SeriesId                     string          `json:",omitempty"`
	SeriesName                   string          `json:",omitempty"`
	SeasonId                     string          `json:",omitempty"`
	SeasonName                   string          `json:",omitempty"`
	Album                        string          `json:",omitempty"`
	AlbumId                      string          `json:",omitempty"`
	AlbumArtist                  string          `json:",omitempty"`
	AlbumArtists                 []embyNameID
	Artists                      []string
	ArtistItems                  []embyNameID
	Genres                       []string
	GenreItems                   []embyNameID
	Studios                      []embyNameID
	People                       []any
	MediaStreams                 []any
	Taglines                     []string
	ProductionLocations          []string
	Keywords                     []string
	RemoteTrailers               []any
	ExternalUrls                 []any
	LockedFields                 []string
	Path                         string          `json:",omitempty"`
	ImageTags                    map[string]string
	BackdropImageTags            []string
	ProviderIds                  map[string]string
	UserData                     *embyUserData
	MediaSources                 []embyMediaSource
	ChildCount                   int             `json:",omitempty"`
	LocationType                 string          `json:",omitempty"`
	CollectionType               string          `json:",omitempty"`
	ViewType                     string          `json:",omitempty"`
	// Subviews 是电影库页面的必需字段：客户端 videos.js 直接读取
	// item.Subviews 并调用 .includes()，缺失会抛 undefined.includes 导致整页报错
	Subviews []string `json:",omitempty"`
	EndDate                      string          `json:",omitempty"`
	Status                       string          `json:",omitempty"`
	PlayAccess                   string          `json:",omitempty"`
	Video3DFormat                string          `json:",omitempty"`
	PreferredMetadataLanguage    string          `json:",omitempty"`
	PreferredMetadataCountryCode string          `json:",omitempty"`
	CleanName                    string          `json:",omitempty"`
	PresentationUniqueKey        string          `json:",omitempty"`
	ShortOverview                string          `json:",omitempty"`
	HomePageUrl                  string          `json:",omitempty"`
	SeriesPrimaryImageTag        string          `json:",omitempty"`
	PrimaryImageTag              string          `json:",omitempty"`
	ParentThumbItemId            string          `json:",omitempty"`
	ParentThumbImageTag          string          `json:",omitempty"`
	ParentPrimaryImageItemId     string          `json:",omitempty"`
	ParentPrimaryImageTag        string          `json:",omitempty"`
	AlbumPrimaryImageTag         string          `json:",omitempty"`
	ParentBackdropItemId         string          `json:",omitempty"`
	AirTime                      string          `json:",omitempty"`
	PrimaryImageAspectRatio      float64         `json:",omitempty"`
	CriticRating                 float64         `json:",omitempty"`
	MediaSourceCount             int             `json:",omitempty"`
	LocalTrailerCount            int             `json:",omitempty"`
	RecursiveItemCount           int             `json:",omitempty"`
	SpecialFeatureCount          int             `json:",omitempty"`
	PartCount                    int             `json:",omitempty"`
	IsHD                         bool
	CanDelete                    bool
	CanDownload                  bool
	SupportsSync                 bool
	IsPlaceHolder                bool
	LockData                     bool
	ImageBlurHashes              map[string]any
	TagItems                     []embyNameID
	AirDays                      []string
	ParentBackdropImageTags      []string
	OriginalTitle                string          `json:",omitempty"`
	ForcedSortName               string          `json:",omitempty"`
	Etag                         string          `json:",omitempty"`
	DateLastMediaAdded           string          `json:",omitempty"`
	CustomRating                 string          `json:",omitempty"`
	ChannelId                    string          `json:",omitempty"`
	ChannelName                  string          `json:",omitempty"`
	FileName                     string          `json:",omitempty"`
	Number                       string          `json:",omitempty"`
	SeriesStudio                 string          `json:",omitempty"`
	AspectRatio                  string          `json:",omitempty"`
	DisplayPreferencesId         string          `json:",omitempty"`
	ParentLogoItemId             string          `json:",omitempty"`
	ParentLogoImageTag           string          `json:",omitempty"`
	IsoType                      string          `json:",omitempty"`
	Software                     string          `json:",omitempty"`
	CameraMake                   string          `json:",omitempty"`
	CameraModel                  string          `json:",omitempty"`
	Size                         int64           `json:",omitempty"`
	Bitrate                      int             `json:",omitempty"`
	IndexNumberEnd               int             `json:",omitempty"`
	DisplayOrder                 string          `json:",omitempty"`
	Width                        int             `json:",omitempty"`
	Height                       int             `json:",omitempty"`
	Chapters                     []any
	Tags                         []string
}

// MarshalJSON 把 nil 切片/映射输出为空数组/空对象。
// Emby 客户端会对部分字段直接调用 .includes()，
// JSON 里出现 null 或字段缺失都会让客户端抛异常。
func (e embyItem) MarshalJSON() ([]byte, error) {
	type alias embyItem
	a := alias(e)
	if a.AlbumArtists == nil {
		a.AlbumArtists = []embyNameID{}
	}
	if a.Artists == nil {
		a.Artists = []string{}
	}
	if a.ArtistItems == nil {
		a.ArtistItems = []embyNameID{}
	}
	if a.Genres == nil {
		a.Genres = []string{}
	}
	if a.GenreItems == nil {
		a.GenreItems = []embyNameID{}
	}
	if a.Studios == nil {
		a.Studios = []embyNameID{}
	}
	if a.People == nil {
		a.People = []any{}
	}
	if a.MediaStreams == nil {
		a.MediaStreams = []any{}
	}
	if a.Taglines == nil {
		a.Taglines = []string{}
	}
	if a.ProductionLocations == nil {
		a.ProductionLocations = []string{}
	}
	if a.Keywords == nil {
		a.Keywords = []string{}
	}
	if a.RemoteTrailers == nil {
		a.RemoteTrailers = []any{}
	}
	if a.ExternalUrls == nil {
		a.ExternalUrls = []any{}
	}
	if a.LockedFields == nil {
		a.LockedFields = []string{}
	}
	if a.ImageTags == nil {
		a.ImageTags = map[string]string{}
	}
	// 没有封面时不输出 Primary：空 tag 会让 Theater 拼出 ?tag= 的无效图片请求，
	// openemby_tv 等客户端也只在 tag 非空时才请求图片
	if v, ok := a.ImageTags["Primary"]; ok && v == "" {
		delete(a.ImageTags, "Primary")
	}
	// 剧集/歌曲等子项没有自己的封面时，让客户端回退到父级封面，保证列表不出现空白图
	if a.ImageTags["Primary"] == "" && a.ParentPrimaryImageTag != "" {
		if a.ParentPrimaryImageItemId == "" {
			a.ParentPrimaryImageItemId = a.ParentId
		}
	}
	if a.BackdropImageTags == nil {
		a.BackdropImageTags = []string{}
	}
	if a.ProviderIds == nil {
		a.ProviderIds = map[string]string{}
	}
	if a.MediaSources == nil {
		a.MediaSources = []embyMediaSource{}
	}
	if a.ImageBlurHashes == nil {
		a.ImageBlurHashes = map[string]any{}
	}
	if a.TagItems == nil {
		a.TagItems = []embyNameID{}
	}
	if a.AirDays == nil {
		a.AirDays = []string{}
	}
	if a.ParentBackdropImageTags == nil {
		a.ParentBackdropImageTags = []string{}
	}
	if a.Chapters == nil {
		a.Chapters = []any{}
	}
	if a.Tags == nil {
		a.Tags = []string{}
	}
	if a.UserData == nil {
		a.UserData = &embyUserData{}
	}
	// PlayAccess 为空会让客户端判定"播放受限"
	if a.PlayAccess == "" {
		a.PlayAccess = "Full"
	}
	if !a.IsFolder {
		a.CanDownload = true
	}
	// 客户端会直接展示 CleanName，并把它当作排序/查找键
	if a.CleanName == "" {
		a.CleanName = a.Name
	}
	if a.PresentationUniqueKey == "" {
		a.PresentationUniqueKey = a.Id
	}
	return json.Marshal(a)
}

type embyQueryResult struct {
	Items            []*embyItem
	TotalRecordCount int
	StartIndex       int
}

type embyUserDto struct {
	Name                  string
	ServerId              string
	Id                    string
	HasPassword           bool
	HasConfiguredPassword bool
	Configuration         map[string]any
	Policy                map[string]any
	PrimaryImageTag       string `json:",omitempty"`
	// DateTime? 字段空值必须省略
	LastLoginDate    string `json:",omitempty"`
	LastActivityDate string `json:",omitempty"`
	IsAdministrator  bool
	IsDisabled       bool
}

type embyAuthResult struct {
	User        *model.User
	AccessToken string
	ServerId    string
}

// embyPublicUserDto 登录前展示的用户信息。
// Emby 客户端依赖 /Users/Public 渲染用户选择列表，返回空数组会跳过登录界面。
type embyPublicUserDto struct {
	Name            string
	ServerId        string
	Id              string
	HasPassword     bool
	PrimaryImageTag string
}

// ==================== 基础工具 ====================

func embyServerID() string {
	seed := conf.Conf.SiteURL + conf.Conf.JwtSecret + strconv.Itoa(conf.Conf.Scheme.HttpPort)
	sum := sha1.Sum([]byte("openlist-emby:" + seed))
	// Emby 的 ServerId 是 32 位 hex GUID，客户端本地库会用 Guid 解析
	return hex.EncodeToString(sum[:16])
}

// embyCompatVersion 是返回给 Emby 客户端的兼容版本号。
// Emby 客户端会把该值与其要求的最低服务器版本比较，直接返回 OpenList 版本
// （如 v4.1.0 / dev）会被判定为"服务器需要升级"而拒绝连接。
// 客户端在 4.10.0.4 起改用服务端 HomeSections API；
// 低于该版本会走本地生成逻辑，其 section 缺少 ListOptions.fields 会直接报错。
const embyCompatVersion = "4.10.0.5"

func embyVersion() string {
	return embyCompatVersion
}

func embyTicks(seconds int) int64 {
	if seconds <= 0 {
		return 0
	}
	return int64(seconds) * 10_000_000
}

func embyTimeStr(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.0000000Z")
}

func embyExt(name string) string {
	ext := strings.TrimPrefix(strings.ToLower(stdpath.Ext(name)), ".")
	if ext == "" {
		return "mkv"
	}
	return ext
}

var embyAuthHeaderRe = regexp.MustCompile(`(?i)([A-Za-z]+)\s*=\s*"([^"]*)"`)

func embyParseAuthHeader(v string) map[string]string {
	out := map[string]string{}
	for _, m := range embyAuthHeaderRe.FindAllStringSubmatch(v, -1) {
		out[strings.ToLower(m[1])] = m[2]
	}
	return out
}

func embyRequestToken(c *gin.Context) string {
	// Emby Windows 等客户端会把 X-Emby-Token 直接放在 URL query 里，
	// 必须与 api_key / ApiKey 一起解析，否则登录成功后每个请求仍是 401
	for key, values := range c.Request.URL.Query() {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		switch {
		case strings.EqualFold(key, "api_key"),
			strings.EqualFold(key, "ApiKey"),
			strings.EqualFold(key, "X-Emby-Token"),
			strings.EqualFold(key, "X-MediaBrowser-Token"):
			return strings.TrimSpace(values[0])
		}
	}
	for _, h := range []string{"X-Emby-Token", "X-MediaBrowser-Token"} {
		if t := strings.TrimSpace(c.GetHeader(h)); t != "" {
			return t
		}
	}
	if v := c.GetHeader("X-Emby-Authorization"); v != "" {
		if t, ok := embyParseAuthHeader(v)["token"]; ok && t != "" {
			return t
		}
	}
	if v := c.GetHeader("Authorization"); strings.HasPrefix(strings.ToLower(v), "mediabrowser ") {
		if t, ok := embyParseAuthHeader(v)["token"]; ok && t != "" {
			return t
		}
	}
	return ""
}

// embyAuth 解析请求身份，token 可以是设备会话令牌或媒体应用密钥
func embyAuth(c *gin.Context) (*model.User, bool) {
	token := embyRequestToken(c)
	if token == "" {
		return nil, false
	}
	if dev, err := db.GetEmbyDeviceByTokenHash(media.HashToken(token)); err == nil {
		db.TouchEmbyDevice(dev.ID)
		user, err := op.GetUserById(dev.UserID)
		if err != nil || user.Disabled {
			return nil, false
		}
		return user, true
	}
	user, _, ok := LookUpAppKeyUser(token)
	if !ok {
		return nil, false
	}
	return user, true
}

// embyUserGUID 返回给客户端的用户 ID（Emby 规范为 32 位 hex GUID）
func embyUserGUID(id uint) string {
	return embyGUID("user-" + strconv.FormatUint(uint64(id), 10))
}

// embyResolveUserID 兼容数字 ID 与 GUID 两种形式
func embyResolveUserID(idStr string) (uint, bool) {
	if n, err := strconv.ParseUint(idStr, 10, 64); err == nil {
		return uint(n), true
	}
	users, _, err := op.GetUsers(1, -1)
	if err != nil {
		return 0, false
	}
	for i := range users {
		if embyUserGUID(users[i].ID) == idStr {
			return users[i].ID, true
		}
	}
	return 0, false
}

func embyUserDtoOf(u *model.User) *embyUserDto {
	// Emby 客户端的 PlayAccessValidation 会直接读取 Policy.EnableMediaPlayback，
	// 缺失即判定为禁止播放，并弹出 "Playback of this content is currently restricted"。
	// 这里按 Emby 的 UserPolicy 结构给全默认值，避免任何字段被读成 undefined。
	policy := map[string]any{
		"IsAdministrator":                u.IsAdmin(),
		"IsDisabled":                     u.Disabled,
		"IsHidden":                       false,
		"EnableMediaPlayback":            true,
		"EnableAudioPlaybackTranscoding": true,
		"EnableVideoPlaybackTranscoding": true,
		"EnablePlaybackRemuxing":         true,
		"EnableContentDownloading":       true,
		"EnableSyncTranscoding":          true,
		"EnableSubtitleDownloading":      true,
		"EnableSubtitleManagement":       true,
		"EnableRemoteControlOfOtherUsers": false,
		"EnableSharedDeviceControl":      true,
		"EnableRemoteAccess":             true,
		"EnableLiveTvManagement":         false,
		"EnableLiveTvAccess":             false,
		"EnableAllChannels":              true,
		"EnableAllFolders":               true,
		"EnableAllDevices":               true,
		"ForceRemoteSourceTranscoding":   false,
		"EnabledFolders":                 []string{},
		"EnabledChannels":                []string{},
		"EnabledDevices":                 []string{},
		"BlockedChannels":                []string{},
		"BlockedMediaFolders":            []string{},
		"BlockedTags":                    []string{},
		"AllowedTags":                    []string{},
		"MaxParentalRating":              nil,
		"AuthenticationProviderId":       "Default",
		"PasswordResetProviderId":        "Default",
	}
	return &embyUserDto{
		Name:                  u.Username,
		ServerId:              embyServerID(),
		Id:                    embyUserGUID(u.ID),
		HasPassword:           u.PwdHash != "",
		HasConfiguredPassword: u.PwdHash != "",
		Configuration:         map[string]any{},
		Policy:                policy,
		IsAdministrator:       u.IsAdmin(),
		IsDisabled:            u.Disabled,
	}
}

// embyWebNoticeHTML 浏览器直接打开 @emby 时显示的提示页。
// OpenList 只实现 Emby API，没有 Emby Web 客户端静态资源。
const embyWebNoticeHTML = `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>OpenList - Emby API</title></head>
<body style="font-family:system-ui,sans-serif;max-width:720px;margin:64px auto;padding:0 20px;line-height:1.8;color:#222">
<h2>OpenList 仅提供 Emby 兼容 API</h2>
<p>当前地址没有 Emby Web 客户端界面，请使用支持 Emby 协议的客户端添加服务器。</p>
<table style="border-collapse:collapse">
<tr><td style="padding:4px 12px 4px 0;color:#666">服务器地址</td><td><code>http://你的地址:端口/@emby</code></td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#666">用户名</td><td>OpenList 用户名</td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#666">密码</td><td>媒体应用密钥（管理 - 媒体库 - 应用密钥）</td></tr>
</table>
<p style="color:#666">推荐客户端：Infuse、VidHub、SenPlayer、Emby 官方 App 等。</p>
<hr style="margin:24px 0;border:none;border-top:1px solid #eee">
<p style="color:#888;font-size:14px">This endpoint only serves the Emby-compatible API. Please use an Emby client instead of a browser.</p>
</body></html>`

// ==================== 请求分发 ====================

// EmbyAPI Emby 兼容 API 入口，兼容 /@emby 与 /@mediabrowser 前缀，路径大小写不敏感
func EmbyAPI(c *gin.Context) {
	if !MediaEmbyEnabled() {
		c.AbortWithStatus(404)
		return
	}
	raw := strings.Trim(c.Param("path"), "/")
	if raw == "" {
		c.JSON(200, gin.H{"Service": "OpenList", "Version": embyVersion()})
		return
	}
	// 兼容把接入地址填成 .../@emby 后客户端又自动追加 /emby（或 /mediabrowser）的情况
	if first, rest, found := strings.Cut(raw, "/"); found {
		switch strings.ToLower(first) {
		case "emby", "mediabrowser":
			raw = rest
		}
	}
	segs := strings.Split(raw, "/")
	lower := make([]string, len(segs))
	for i, s := range segs {
		lower[i] = strings.ToLower(s)
	}
	method := c.Request.Method

	// 公共端点：无需认证
	switch {
	case len(lower) == 3 && lower[0] == "system" && lower[1] == "info" && lower[2] == "public":
		embyPublicSystemInfo(c)
		return
	case len(lower) == 2 && lower[0] == "system" && lower[1] == "ping":
		c.String(200, "Emby Server")
		return
	// 客户端连接阶段还会拉取公共配置，返回空对象即可，否则同样会 401
	case len(lower) == 3 && lower[0] == "system" && lower[1] == "configuration" && lower[2] == "public":
		c.JSON(200, gin.H{})
		return
	case len(lower) == 2 && lower[0] == "branding" && lower[1] == "configuration":
		c.JSON(200, gin.H{})
		return
	// 离线同步接口：OpenList 不支持客户端下载同步，返回空结果而不是 401
	case len(lower) >= 1 && lower[0] == "sync":
		if method == "GET" {
			c.JSON(200, []any{})
		} else {
			c.Status(204)
		}
		return
	// 浏览器直接打开 @emby 时会拉取 Emby Web 静态资源，这里返回说明页而不是 401
	case lower[0] == "web":
		if strings.HasSuffix(strings.ToLower(c.Request.URL.Path), ".json") {
			c.JSON(200, gin.H{})
			return
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(200, embyWebNoticeHTML)
		return
	// 没有 token 的连接探测：部分客户端直接请求 /System/Info（非 Public）。
	// 必须返回完整结构（含 WebPath），精简版会让客户端在处理 sections 时崩溃
	case len(lower) == 2 && lower[0] == "system" && lower[1] == "info":
		embySystemInfo(c)
		return
	case len(lower) == 2 && lower[0] == "system" && lower[1] == "endpoint":
		// 按实际来源判断，不要固定返回 true：硬编码「内网直连」会让客户端
		// 误以为可直连并跳过中转，也是对匿名访问者宣称内网可达。
		local := embyIsLocalRequest(c)
		c.JSON(200, gin.H{"IsLocal": local, "IsInNetwork": local})
		return
	case len(lower) == 2 && lower[0] == "localization" && lower[1] == "options":
		c.JSON(200, []any{})
		return
	case len(lower) == 2 && lower[0] == "sessions" && lower[1] == "capabilities":
		c.Status(204)
		return
	case len(lower) == 3 && lower[0] == "sessions" && lower[1] == "capabilities" && lower[2] == "full":
		c.Status(204)
		return
	// 播放进度上报：部分客户端的原生播放器不带 token，
	// 这类请求不能 401，否则客户端会中断播放状态跟踪
	case len(lower) >= 2 && lower[0] == "sessions" && lower[1] == "playing":
		if user, ok := embyAuth(c); ok {
			embySessionPlaying(c, user, strings.Join(lower[2:], "/"))
		} else {
			c.Status(204)
		}
		return
	// 电影库页面可能请求的推荐/字母索引接口，返回空结果
	case method == "GET" && len(lower) == 2 && lower[0] == "movies" && lower[1] == "recommendations":
		c.JSON(200, []any{})
		return
	case method == "GET" && len(lower) == 2 && lower[0] == "items" && lower[1] == "prefixes":
		c.JSON(200, []any{})
		return
	// 添加服务器 / 登录流程必须在鉴权前放行，否则客户端只会看到 Unauthorized
	case len(lower) == 2 && lower[0] == "users" && lower[1] == "public":
		embyPublicUsers(c)
		return
	case len(lower) == 2 && lower[0] == "users" && lower[1] == "authenticatebyname":
		embyAuthenticateByName(c)
		return
	// 旧版客户端登录：POST /Users/{userId}/Authenticate，密码在 body 或 query
	case len(lower) == 3 && lower[0] == "users" && lower[2] == "authenticate":
		embyAuthenticateByID(c, segs[1])
		return
	case len(lower) >= 3 && lower[0] == "items" && strings.EqualFold(lower[2], "images"):
		// /Items/{id}/Images/Primary 等：图片 URL 不带 token，必须匿名可访问
		embyImage(c, segs[1], segs, true)
		return
	}

	user, ok := embyAuth(c)
	if !ok {
		embyUnauthorized(c)
		return
	}

	switch {
	// Emby 客户端用它做会话与实时通知，缺失时会持续重连
	case len(lower) == 1 && lower[0] == "embywebsocket":
		embyWebSocket(c)
	// 客户端启动/仪表盘探测接口，返回空数据避免 404 弹通用错误
	case method == "GET" && len(lower) == 1 && lower[0] == "sessions":
		c.JSON(200, []any{})
	case method == "GET" && len(lower) == 1 && lower[0] == "scheduledtasks":
		c.JSON(200, []any{})
	case len(lower) == 3 && lower[0] == "system" && lower[1] == "activitylog" && lower[2] == "entries":
		c.JSON(200, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
	// LiveTv：会话页/剧集页会查询录像、节目单等，全部返回空结果
	case len(lower) >= 2 && lower[0] == "livetv":
		c.JSON(200, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
	// Emby 4.10 客户端首页读取用户设置：GET /usersettings/{userId}
	case len(lower) == 2 && lower[0] == "usersettings":
		if method == "GET" {
			c.JSON(200, gin.H{
				"Screensaver":  gin.H{},
				"HomeSections": []any{},
				"Playback":     gin.H{},
			})
		} else {
			c.Status(204)
		}
	// 客户端登录后会读取/保存界面偏好，字段缺失会让 sectionscontroller 抛异常
	case len(lower) >= 1 && lower[0] == "displaypreferences":
		if method == "GET" {
			c.JSON(200, gin.H{
				"Id":                 "usersettings",
				"ViewType":           "",
				"SortBy":             "SortName",
				"IndexBy":            "",
				"RememberIndexing":   false,
				"PrimaryImageHeight": 250,
				"PrimaryImageWidth":  250,
				"CustomPrefs": gin.H{
					"homeSections": "",
				},
				"ScrollDirection": "Horizontal",
				"ShowBackdrop":    true,
				"RememberSorting": false,
				"SortOrder":       "Ascending",
				"ShowSidebar":     false,
				"Client":          "emby",
			})
		} else {
			c.Status(204)
		}
	case len(lower) == 2 && lower[0] == "system" && lower[1] == "info":
		embySystemInfo(c)
	case len(lower) == 2 && lower[0] == "system" && lower[1] == "endpoint":
		c.JSON(200, gin.H{"IsLocal": true, "IsInNetwork": true})
	case len(lower) == 1 && lower[0] == "users" && method == "GET":
		embyListUsers(c, user)
	case len(lower) == 2 && lower[0] == "users" && lower[1] == "query":
		embyQueryUsers(c)
	case len(lower) == 2 && lower[0] == "users" && method == "GET":
		embyGetUser(c, segs[1], user)
	case len(lower) == 3 && lower[0] == "users" && lower[2] == "views":
		embyViews(c, user)
	case len(lower) == 2 && lower[0] == "items" && lower[1] == "counts":
		embyItemCounts(c, user)
	case len(lower) == 2 && lower[0] == "library" && lower[1] == "refresh" && method == "POST":
		embyLibraryRefresh(c)
	case len(lower) == 2 && lower[0] == "library" && lower[1] == "selectablemediafolders":
		embySelectableFolders(c, user)
	case len(lower) == 3 && lower[0] == "library" && lower[1] == "virtualfolders" && lower[2] == "query":
		embyVirtualFolders(c, user)
	case len(lower) == 2 && lower[0] == "search" && lower[1] == "hints":
		embySearchHints(c, user)
	// 首页「收藏的艺术家 / 人物」分区查询，无对应数据时返回空结果
	case method == "GET" && len(lower) == 1 && lower[0] == "artists":
		c.JSON(200, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
	case method == "GET" && len(lower) == 1 && lower[0] == "persons":
		c.JSON(200, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
	case len(lower) == 2 && lower[0] == "artists":
		embyArtists(c, user, lower[1] == "albumartists")
	case len(lower) == 3 && lower[0] == "sessions" && lower[1] == "playing":
		embySessionPlaying(c, user, lower[2])
	case len(lower) == 2 && lower[0] == "sessions" && lower[1] == "playing" && method == "POST":
		embySessionPlaying(c, user, "")
	case len(lower) == 2 && lower[0] == "sessions" && lower[1] == "logout" && method == "POST":
		embyLogout(c, user)
	case len(lower) == 2 && lower[0] == "sessions" && lower[1] == "capabilities":
		c.Status(204)
	case len(lower) == 3 && lower[0] == "sessions" && lower[1] == "capabilities" && lower[2] == "full":
		c.Status(204)
	// 媒体库页（电影/剧集/音乐）筛选栏依赖的分面接口，缺失返回 404 会让整页报错
	case method == "GET" && len(lower) == 2 && lower[0] == "items" && (lower[1] == "filters" || lower[1] == "filters2"):
		c.JSON(200, gin.H{"Genres": []any{}, "Tags": []any{}, "OfficialRatings": []any{}, "Years": []any{}})
	case method == "GET" && len(lower) == 1 && embyEmptyListRoot[lower[0]]:
		c.JSON(200, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
	// 用户态条目
	case len(lower) >= 3 && lower[0] == "users":
		embyUserScoped(c, user, segs, lower, method)
	// 全局条目
	case len(lower) >= 2 && lower[0] == "items":
		embyItemsScoped(c, user, segs, lower, method)
	// 播放流
	case len(lower) >= 3 && (lower[0] == "videos" || lower[0] == "audio") &&
		(lower[2] == "stream" || strings.HasPrefix(lower[2], "stream.") || lower[2] == "universal"):
		embyStream(c, user, segs[1], lower[0] == "audio")
	case len(lower) == 3 && lower[0] == "audio" && lower[2] == "lyrics":
		embyLyrics(c, user, segs[1])
	// 剧集：/Shows/NextUp 只有两段，/Shows/{id}/Seasons 为三段
	case len(lower) >= 2 && lower[0] == "shows":
		embyShows(c, user, segs, lower, method)
	default:
		embyNotFound(c)
	}
}

func embyUnauthorized(c *gin.Context) {
	// 记录客户端实际请求，便于排查"连接后被判未认证"的兼容问题。
	// 只能记 Path 不能记 RequestURI：query 里可能带 Pw/Password/api_key，
	// 而 X-Emby-Authorization 里是可直接使用的设备令牌，都不能落到日志里。
	utils.Log.Warnf("emby unauthorized: %s %s client=%q ua=%q hasToken=%v",
		c.Request.Method, c.Request.URL.Path, c.ClientIP(),
		c.GetHeader("User-Agent"), embyRequestToken(c) != "")
	c.JSON(401, gin.H{"Error": "Unauthorized", "ResponseStatus": gin.H{"ErrorCode": "Unauthorized"}})
}

func embyNotFound(c *gin.Context) {
	c.JSON(404, gin.H{"Error": "Not Found"})
}

// embyEmptyListRoot 本服务无对应数据、但客户端页面一定会请求的顶层集合接口
var embyEmptyListRoot = map[string]bool{
	"genres": true, "musicgenres": true, "studios": true, "tags": true,
	"officialratings": true, "years": true, "collections": true,
	"playlists": true, "trailers": true, "channels": true, "livetv": true,
}

// embyAuxItem 处理详情页附带请求（相似、预告片、特辑、片头等）。
// 这类接口缺失时客户端详情页和列表页都会弹"error processing the request"。
// 返回 true 表示已响应。
func embyAuxItem(c *gin.Context, name string) bool {
	switch name {
	case "similar", "suggestions", "instantmix", "intros":
		c.JSON(200, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
	case "localtrailers", "specialfeatures", "additionalparts", "remoteimages", "thememedia":
		c.JSON(200, []any{})
	case "criticreviews":
		c.JSON(200, gin.H{"Items": []any{}, "TotalRecordCount": 0})
	default:
		return false
	}
	return true
}

var embyWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// embyWebSocket 提供最小可用的 Emby WebSocket 通道。
// 客户端用它做会话/实时通知，缺失时会持续重连并产生大量 404。
func embyWebSocket(c *gin.Context) {
	conn, err := embyWSUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(4096)
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"MessageType":"KeepAlive"}`)); err != nil {
					return
				}
			case <-stop:
				return
			}
		}
	}()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// ==================== 系统 / 用户 ====================

func embyPublicSystemInfo(c *gin.Context) {
	api := common.GetApiUrl(c.Request.Context())
	c.JSON(200, gin.H{
		"ServerName":             setting.GetStr(conf.SiteTitle, "OpenList"),
		"Version":                embyVersion(),
		"Id":                     embyServerID(),
		"OperatingSystem":        "Linux",
		"LocalAddress":           api,
		"WebPath":                "",
		"StartupWizardCompleted": true,
	})
}

func embySystemInfo(c *gin.Context) {
	api := common.GetApiUrl(c.Request.Context())
	c.JSON(200, gin.H{
		"ServerName":               setting.GetStr(conf.SiteTitle, "OpenList"),
		"Version":                  embyVersion(),
		"Id":                       embyServerID(),
		"OperatingSystem":          "Linux",
		"OperatingSystemDisplayName": "Linux",
		"LocalAddress":             api,
		"WebPath":                  "",
		"StartupWizardCompleted":   true,
		"HasPendingRestart":        false,
		"IsShuttingDown":           false,
		"SupportsLibraryMonitor":   false,
		"CanSelfRestart":           false,
		"CanLaunchWebBrowser":      false,
		"SupportsHttps":            false,
		"HasUpdateAvailable":       false,
		"EncoderLocation":          "System",
		"SystemArchitecture":       "X64",
		"SystemUpdateLevel":        "Release",
		// 不返回真实监听端口：这个端点匿名可访问，客户端本来就是从已知端口连进来的，
		// 回了真实端口只是白给一份端口探测信息。
		"HttpServerPortNumber": 0,
		"HttpsPortNumber":      0,
		"ProgramDataPath":          "",
		"ItemsByNamePath":          "",
		"CachePath":                "",
		"LogPath":                  "",
		"InternalMetadataPath":     "",
		"TranscodingTempPath":      "",
	})
}

// embyIsLocalRequest 判断请求是否来自本机 / 内网，用于 /System/Endpoint。
func embyIsLocalRequest(c *gin.Context) bool {
	ip := net.ParseIP(c.ClientIP())
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

// embyReadAuthBody 读取登录凭据，兼容 JSON / 表单 / query 三种提交方式
func embyReadAuthBody(c *gin.Context) (username, pw string) {
	if strings.Contains(strings.ToLower(c.ContentType()), "json") {
		var req struct {
			Username string `json:"Username"`
			Pw       string `json:"Pw"`
			Password string `json:"Password"`
		}
		_ = c.ShouldBindJSON(&req)
		username, pw = req.Username, req.Pw
		if pw == "" {
			pw = req.Password
		}
	} else {
		_ = c.Request.ParseForm()
		username = c.PostForm("Username")
		pw = c.PostForm("Pw")
		if pw == "" {
			pw = c.PostForm("Password")
		}
	}
	if username == "" {
		username = c.Query("Username")
	}
	if pw == "" {
		pw = c.Query("Pw")
	}
	if pw == "" {
		pw = c.Query("Password")
	}
	return strings.TrimSpace(username), strings.TrimSpace(pw)
}

// embyAuthenticateByName 标准登录接口 POST /Users/AuthenticateByName
func embyAuthenticateByName(c *gin.Context) {
	username, pw := embyReadAuthBody(c)
	// 除 IP 外还要检查用户名维度：IP 可被 X-Forwarded-For 伪造
	if mediaAuthLockedFor(c, username) {
		c.JSON(429, gin.H{"Error": model.TooManyAttempts})
		return
	}
	if username == "" || pw == "" {
		embyUnauthorized(c)
		return
	}
	user, err := op.GetUserByName(username)
	if err != nil || user.Disabled || user.IsGuest() {
		mediaAuthFailedFor(c, username)
		embyUnauthorized(c)
		return
	}
	embyCompleteAuth(c, user, pw)
}

// embyAuthenticateByID 旧版登录接口 POST /Users/{userId}/Authenticate
func embyAuthenticateByID(c *gin.Context, idStr string) {
	if mediaAuthLocked(c) {
		c.JSON(429, gin.H{"Error": model.TooManyAttempts})
		return
	}
	id, ok := embyResolveUserID(idStr)
	if !ok {
		embyUnauthorized(c)
		return
	}
	user, err := op.GetUserById(id)
	if err != nil || user.Disabled || user.IsGuest() {
		mediaAuthFailed(c)
		embyUnauthorized(c)
		return
	}
	// 按 id 登录同样要受用户名维度的锁定约束，否则可以换 id 绕过按用户名的计数
	if mediaAuthLockedFor(c, user.Username) {
		c.JSON(429, gin.H{"Error": model.TooManyAttempts})
		return
	}
	_, pw := embyReadAuthBody(c)
	if pw == "" {
		embyUnauthorized(c)
		return
	}
	embyCompleteAuth(c, user, pw)
}

// embyCompleteAuth 校验密码并签发设备令牌。
// 密码位置既接受 OpenList 登录密码（无 2FA 时），也接受属于该用户的应用密钥。
func embyCompleteAuth(c *gin.Context, user *model.User, pw string) {
	keyUser, _, keyOK := LookUpAppKeyUser(pw)
	switch {
	case keyOK && keyUser.ID == user.ID:
		// 应用密钥认证通过，2FA 账号也允许（密钥本身即第二因素）
	case keyOK:
		mediaAuthFailedFor(c, user.Username)
		embyUnauthorized(c)
		return
	case user.OtpSecret != "":
		// 2FA 账号不允许用登录密码通过 Emby 认证
		mediaAuthFailedFor(c, user.Username)
		c.JSON(403, gin.H{"Error": "Accounts with two-factor authentication must use an app key"})
		return
	case user.ValidateRawPassword(pw) != nil:
		mediaAuthFailedFor(c, user.Username)
		embyUnauthorized(c)
		return
	}
	mediaAuthSucceededFor(c, user.Username)
	authHeader := embyParseAuthHeader(c.GetHeader("X-Emby-Authorization"))
	token, err := media.NewDeviceToken()
	if err != nil {
		c.JSON(500, gin.H{"Error": err.Error()})
		return
	}
	dev := &model.EmbyDevice{
		UserID:     user.ID,
		DeviceID:   authHeader["deviceid"],
		DeviceName: authHeader["device"],
		Client:     authHeader["client"],
		Version:    authHeader["version"],
		TokenHash:  media.HashToken(token),
		LastSeenAt: time.Now(),
	}
	_ = db.CreateEmbyDevice(dev)
	c.JSON(200, gin.H{
		"User":        embyUserDtoOf(user),
		"AccessToken": token,
		"ServerId":    embyServerID(),
	})
}

// embyPublicUsers 匿名可访问，只返回登录界面需要的公开字段，
// 不包含权限、策略、是否管理员等敏感信息。
func embyPublicUsers(c *gin.Context) {
	out := make([]*embyPublicUserDto, 0)
	users, _, err := op.GetUsers(1, -1)
	if err == nil {
		for i := range users {
			if users[i].IsGuest() || users[i].Disabled {
				continue
			}
			out = append(out, &embyPublicUserDto{
				Name:        users[i].Username,
				ServerId:    embyServerID(),
				Id:          embyUserGUID(users[i].ID),
				HasPassword: users[i].PwdHash != "",
			})
		}
	}
	c.JSON(200, out)
}

func embyListUsers(c *gin.Context, current *model.User) {
	users, _, err := op.GetUsers(1, -1)
	if err != nil {
		c.JSON(500, gin.H{"Error": err.Error()})
		return
	}
	out := make([]*embyUserDto, 0, len(users))
	for i := range users {
		if users[i].IsGuest() {
			continue
		}
		if !current.IsAdmin() && users[i].ID != current.ID {
			continue
		}
		out = append(out, embyUserDtoOf(&users[i]))
	}
	c.JSON(200, out)
}

func embyQueryUsers(c *gin.Context) {
	users, _, err := op.GetUsers(1, -1)
	if err != nil {
		c.JSON(500, gin.H{"Error": err.Error()})
		return
	}
	out := make([]*embyUserDto, 0, len(users))
	for i := range users {
		if users[i].IsGuest() {
			continue
		}
		out = append(out, embyUserDtoOf(&users[i]))
	}
	c.JSON(200, gin.H{"Items": out, "TotalRecordCount": len(out), "StartIndex": 0})
}

func embyGetUser(c *gin.Context, idStr string, current *model.User) {
	// Jellyfin/部分 Emby 客户端使用 /Users/Me 获取当前用户
	if strings.EqualFold(idStr, "me") {
		c.JSON(200, embyUserDtoOf(current))
		return
	}
	id, ok := embyResolveUserID(idStr)
	if !ok {
		embyNotFound(c)
		return
	}
	user, err := op.GetUserById(id)
	if err != nil {
		embyNotFound(c)
		return
	}
	c.JSON(200, embyUserDtoOf(user))
}

func embyLogout(c *gin.Context, user *model.User) {
	token := embyRequestToken(c)
	if token != "" {
		if dev, err := db.GetEmbyDeviceByTokenHash(media.HashToken(token)); err == nil {
			_ = db.DeleteEmbyDevice(user.ID, dev.DeviceID)
		}
	}
	c.Status(204)
}

// ==================== 媒体库刷新 ====================

var (
	embyRefreshMu    sync.Mutex
	embyRefreshTimer *time.Timer
)

func scheduleLibraryRefresh() {
	embyRefreshMu.Lock()
	defer embyRefreshMu.Unlock()
	if embyRefreshTimer != nil {
		embyRefreshTimer.Stop()
	}
	embyRefreshTimer = time.AfterFunc(10*time.Second, func() {
		for _, mt := range MediaTypeEnabledList() {
			cfg, err := db.GetMediaConfig(mt)
			if err != nil || cfg == nil {
				continue
			}
			media.ScanMedia(cfg)
		}
	})
}

func embyLibraryRefresh(c *gin.Context) {
	scheduleLibraryRefresh()
	c.Status(204)
}

// ==================== 内部工具 ====================

func embyPaging(c *gin.Context) (start, limit int) {
	start, _ = strconv.Atoi(c.DefaultQuery("StartIndex", "0"))
	limit, _ = strconv.Atoi(c.DefaultQuery("Limit", "100"))
	if start < 0 {
		start = 0
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	return
}

func embyBoolQuery(c *gin.Context, key string) bool {
	v := strings.ToLower(c.Query(key))
	return v == "true" || v == "1"
}

func embyIntQuery(c *gin.Context, key string) int {
	v, _ := strconv.Atoi(c.Query(key))
	return v
}

func embyJson(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func embyHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// embyGUID 把内部可读 ID 映射为 32 位 hex GUID。
// embyGUID 用于用户 ID（Emby 规范为 32 位 hex GUID）。
// 注意：条目的 Item Id 不能用 GUID，见 embyNumericID。
func embyGUID(s string) string {
	sum := md5.Sum([]byte("openlist-emby-id:" + s))
	return hex.EncodeToString(sum[:])
}

// embyNumericID 生成稳定的十进制数字 ID，供条目（影片/剧集/专辑等）使用。
// Emby 真实服务器的 Item Id 就是数字字符串；Emby Theater 的原生
// LocalItemRepository 会把它当 long 解析（System.Number.ThrowFormatException），
// 传 GUID 或 mv-1 这类可读 ID 都会让客户端播放/取图直接抛异常。
func embyNumericID(s string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte("openlist-emby-item:" + s))
	// 取 47 位：既在 .NET long / JS 安全整数范围内，又足够大避免与真实 ID 混淆
	return strconv.FormatUint(h.Sum64()&0x7FFFFFFFFFFF, 10)
}

func embyImageTag(item *model.MediaItem) string {
	if item.Cover == "" {
		return ""
	}
	return embyHash(item.Cover + item.UpdatedAt.String())
}
