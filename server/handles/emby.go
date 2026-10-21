package handles

import (
	"crypto/sha1"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	stdpath "path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/media"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// ==================== 数据结构（Emby 使用 PascalCase 字段名） ====================

type embyNameID struct {
	Name string
	Id   string
}

type embyUserData struct {
	PlaybackPositionTicks int64
	PlayCount             int
	IsFavorite            bool
	Played                bool
	UnplayedItemCount     int
	PlayedPercentage      float64
	LastPlayedDate        string
}

type embyMediaSource struct {
	Protocol             string
	Id                   string
	Path                 string
	Type                 string
	Container            string
	Name                 string
	Size                 int64
	IsRemote             bool
	RunTimeTicks         int64
	SupportsDirectPlay   bool
	SupportsDirectStream bool
	SupportsTranscoding  bool
	DirectStreamUrl      string
}

type embyItem struct {
	Name              string
	ServerId          string
	Id                string
	DateCreated       string
	Container         string
	SortName          string
	PremiereDate      string
	Overview          string
	ProductionYear    int
	CommunityRating   float32
	OfficialRating    string
	RunTimeTicks      int64
	IndexNumber       int
	ParentIndexNumber int
	Type              string
	MediaType         string
	IsFolder          bool
	ParentId          string
	SeriesId          string
	SeriesName        string
	SeasonId          string
	SeasonName        string
	Album             string
	AlbumId           string
	AlbumArtist       string
	AlbumArtists      []embyNameID
	Artists           []string
	ArtistItems       []embyNameID
	Path              string
	ImageTags         map[string]string
	BackdropImageTags []string
	ProviderIds       map[string]string
	UserData          *embyUserData
	MediaSources      []embyMediaSource
	ChildCount        int
	LocationType      string
	CollectionType    string
}

type embyQueryResult struct {
	Items            []*embyItem
	TotalRecordCount int
	StartIndex       int
}

type embyUserDto struct {
	Name                   string
	ServerId               string
	Id                     string
	HasPassword            bool
	HasConfiguredPassword  bool
	Configuration          map[string]any
	Policy                 map[string]any
	PrimaryImageTag        string
	LastLoginDate          string
	LastActivityDate       string
	IsAdministrator        bool
	IsDisabled             bool
}

type embyAuthResult struct {
	User        *model.User
	AccessToken string
	ServerId    string
}

// ==================== 基础工具 ====================

func embyServerID() string {
	seed := conf.Conf.SiteURL + conf.Conf.JwtSecret + strconv.Itoa(conf.Conf.Scheme.HttpPort)
	sum := sha1.Sum([]byte("openlist-emby:" + seed))
	return hex.EncodeToString(sum[:8])
}

func embyVersion() string {
	return conf.Version
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
	if t := c.Query("api_key"); t != "" {
		return t
	}
	if t := c.Query("ApiKey"); t != "" {
		return t
	}
	for _, h := range []string{"X-Emby-Token", "X-MediaBrowser-Token"} {
		if t := c.GetHeader(h); t != "" {
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
	if strings.HasPrefix(token, "olmp_") {
		keys, err := db.ListMediaAppKeysByPrefix(media.AppKeyPrefix(token))
		if err == nil {
			for i := range keys {
				plain, err := media.DecryptAppKey(keys[i].Secret)
				if err != nil {
					continue
				}
				if subtle.ConstantTimeCompare([]byte(plain), []byte(token)) == 1 {
					user, err := op.GetUserById(keys[i].UserID)
					if err != nil || user.Disabled {
						return nil, false
					}
					db.TouchMediaAppKey(keys[i].ID)
					return user, true
				}
			}
		}
	}
	return nil, false
}

func embyUserDtoOf(u *model.User) *embyUserDto {
	policy := map[string]any{
		"IsAdministrator": u.IsAdmin(),
		"EnableAllFolders": true,
		"EnabledFolders":  []string{},
	}
	return &embyUserDto{
		Name:                  u.Username,
		ServerId:              embyServerID(),
		Id:                    strconv.FormatUint(uint64(u.ID), 10),
		HasPassword:           u.PwdHash != "",
		HasConfiguredPassword: u.PwdHash != "",
		Configuration:         map[string]any{},
		Policy:                policy,
		IsAdministrator:       u.IsAdmin(),
		IsDisabled:            u.Disabled,
	}
}

// ==================== 请求分发 ====================

// EmbyAPI Emby 兼容 API 入口，兼容 /emby 与 /mediabrowser 前缀，路径大小写不敏感
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
	case len(lower) == 3 && lower[0] == "items" && strings.EqualFold(lower[2], "images"):
		embyImage(c, segs[1], segs, true)
		return
	}

	user, ok := embyAuth(c)
	if !ok {
		embyUnauthorized(c)
		return
	}

	switch {
	case len(lower) == 2 && lower[0] == "system" && lower[1] == "info":
		embySystemInfo(c)
	case len(lower) == 2 && lower[0] == "system" && lower[1] == "endpoint":
		c.JSON(200, gin.H{"IsLocal": true, "IsInNetwork": true})
	case len(lower) == 2 && lower[0] == "users" && lower[1] == "authenticatebyname":
		embyAuthenticateByName(c)
	case len(lower) == 1 && lower[0] == "users" && method == "GET":
		embyListUsers(c, user)
	case len(lower) == 2 && lower[0] == "users" && lower[1] == "public":
		embyPublicUsers(c)
	case len(lower) == 2 && lower[0] == "users" && lower[1] == "query":
		embyQueryUsers(c)
	case len(lower) == 2 && lower[0] == "users" && method == "GET":
		embyGetUser(c, segs[1])
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
	// 用户态条目
	case len(lower) >= 3 && lower[0] == "users":
		embyUserScoped(c, user, segs, lower, method)
	// 全局条目
	case len(lower) >= 2 && lower[0] == "items":
		embyItemsScoped(c, user, segs, lower, method)
	// 播放流
	case len(lower) >= 3 && (lower[0] == "videos" || lower[0] == "audio") && lower[2] == "stream":
		embyStream(c, user, segs[1], lower[0] == "audio")
	case len(lower) == 3 && lower[0] == "audio" && lower[2] == "lyrics":
		embyLyrics(c, user, segs[1])
	// 剧集
	case len(lower) >= 3 && lower[0] == "shows":
		embyShows(c, user, segs, lower, method)
	default:
		embyNotFound(c)
	}
}

func embyUnauthorized(c *gin.Context) {
	c.JSON(401, gin.H{"Error": "Unauthorized", "ResponseStatus": gin.H{"ErrorCode": "Unauthorized"}})
}

func embyNotFound(c *gin.Context) {
	c.JSON(404, gin.H{"Error": "Not Found"})
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
		"StartupWizardCompleted": true,
	})
}

func embySystemInfo(c *gin.Context) {
	api := common.GetApiUrl(c.Request.Context())
	c.JSON(200, gin.H{
		"ServerName":             setting.GetStr(conf.SiteTitle, "OpenList"),
		"Version":                embyVersion(),
		"Id":                     embyServerID(),
		"OperatingSystem":        "Linux",
		"LocalAddress":           api,
		"StartupWizardCompleted": true,
		"WebPath":                "/web",
	})
}

func embyAuthenticateByName(c *gin.Context) {
	var req struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
		Password string `json:"Password"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Pw == "" {
		req.Pw = req.Password
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Pw == "" {
		embyUnauthorized(c)
		return
	}
	user, err := op.GetUserByName(req.Username)
	if err != nil || user.Disabled || user.IsGuest() {
		embyUnauthorized(c)
		return
	}
	if user.OtpSecret != "" {
		// 2FA 账号不允许通过 Emby 密码登录，请使用应用密钥
		c.JSON(403, gin.H{"Error": "Accounts with two-factor authentication must use an app key"})
		return
	}
	if err := user.ValidateRawPassword(req.Pw); err != nil {
		embyUnauthorized(c)
		return
	}
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

func embyPublicUsers(c *gin.Context) {
	users, _, err := op.GetUsers(1, -1)
	if err != nil {
		c.JSON(500, gin.H{"Error": err.Error()})
		return
	}
	out := make([]*embyUserDto, 0, len(users))
	for i := range users {
		if users[i].Disabled || users[i].IsGuest() {
			continue
		}
		out = append(out, embyUserDtoOf(&users[i]))
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

func embyGetUser(c *gin.Context, idStr string) {
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		embyNotFound(c)
		return
	}
	user, err := op.GetUserById(uint(id))
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

func embyImageTag(item *model.MediaItem) string {
	if item.Cover == "" {
		return ""
	}
	return embyHash(item.Cover + item.UpdatedAt.String())
}
