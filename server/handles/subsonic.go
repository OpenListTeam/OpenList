package handles

import (
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	stdpath "path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/media"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/gin-gonic/gin"
)

const subsonicAPIVersion = "1.16.1"

// ==================== 响应结构 ====================

type subsonicError struct {
	Code    int    `json:"code" xml:"code,attr"`
	Message string `json:"message" xml:"message,attr"`
}

type subsonicResponse struct {
	XMLName xml.Name `json:"-" xml:"subsonic-response"`

	Status        string `json:"status" xml:"status,attr"`
	Version       string `json:"version" xml:"version,attr"`
	Type          string `json:"type" xml:"type,attr"`
	ServerVersion string `json:"serverVersion" xml:"serverVersion,attr"`
	OpenSubsonic  bool   `json:"openSubsonic" xml:"openSubsonic,attr"`

	Error *subsonicError `json:"error,omitempty" xml:"error,omitempty"`

	License                *subsonicLicense      `json:"license,omitempty" xml:"license,omitempty"`
	MusicFolders           *subsonicMusicFolders `json:"musicFolders,omitempty" xml:"musicFolders,omitempty"`
	Indexes                *subsonicIndexes      `json:"indexes,omitempty" xml:"indexes,omitempty"`
	Artists                *subsonicArtists      `json:"artists,omitempty" xml:"artists,omitempty"`
	Artist                 *subsonicArtist       `json:"artist,omitempty" xml:"artist,omitempty"`
	Album                  *subsonicAlbum        `json:"album,omitempty" xml:"album,omitempty"`
	Song                   *subsonicChild        `json:"song,omitempty" xml:"song,omitempty"`
	AlbumList              *subsonicAlbumList    `json:"albumList,omitempty" xml:"albumList,omitempty"`
	AlbumList2             *subsonicAlbumList    `json:"albumList2,omitempty" xml:"albumList2,omitempty"`
	SearchResult2          *subsonicSearchResult `json:"searchResult2,omitempty" xml:"searchResult2,omitempty"`
	SearchResult3          *subsonicSearchResult `json:"searchResult3,omitempty" xml:"searchResult3,omitempty"`
	Playlists              *subsonicPlaylists    `json:"playlists,omitempty" xml:"playlists,omitempty"`
	Playlist               *subsonicPlaylist     `json:"playlist,omitempty" xml:"playlist,omitempty"`
	Lyrics                 *subsonicLyrics       `json:"lyrics,omitempty" xml:"lyrics,omitempty"`
	LyricsList             *subsonicLyricsList   `json:"lyricsList,omitempty" xml:"lyricsList,omitempty"`
	Starred                *subsonicStarred      `json:"starred,omitempty" xml:"starred,omitempty"`
	Starred2               *subsonicStarred      `json:"starred2,omitempty" xml:"starred2,omitempty"`
	Genres                 *subsonicGenres       `json:"genres,omitempty" xml:"genres,omitempty"`
	RandomSongs            *subsonicSongs        `json:"randomSongs,omitempty" xml:"randomSongs,omitempty"`
	SongsByGenre           *subsonicSongs        `json:"songsByGenre,omitempty" xml:"songsByGenre,omitempty"`
	ScanStatus             *subsonicScanStatus   `json:"scanStatus,omitempty" xml:"scanStatus,omitempty"`
	User                   *subsonicUser         `json:"user,omitempty" xml:"user,omitempty"`
	Users                  *subsonicUsers        `json:"users,omitempty" xml:"users,omitempty"`
	OpenSubsonicExtensions *subsonicExtensions   `json:"openSubsonicExtensions,omitempty" xml:"openSubsonicExtensions,omitempty"`
	NowPlaying             *subsonicNowPlaying   `json:"nowPlaying,omitempty" xml:"nowPlaying,omitempty"`
	PlayQueue              *subsonicPlayQueue    `json:"playQueue,omitempty" xml:"playQueue,omitempty"`
	ArtistInfo2            *subsonicArtistInfo   `json:"artistInfo2,omitempty" xml:"artistInfo2,omitempty"`
	AlbumInfo              *subsonicAlbumInfo    `json:"albumInfo,omitempty" xml:"albumInfo,omitempty"`
	TopSongs               *subsonicSongs        `json:"topSongs,omitempty" xml:"topSongs,omitempty"`
	SimilarSongs2          *subsonicSongs        `json:"similarSongs2,omitempty" xml:"similarSongs2,omitempty"`
	MusicDirectory         *subsonicDirectory    `json:"directory,omitempty" xml:"directory,omitempty"`
	Bookmarks              *subsonicBookmarks    `json:"bookmarks,omitempty" xml:"bookmarks,omitempty"`
}

type subsonicLicense struct {
	Valid bool `json:"valid" xml:"valid,attr"`
}

type subsonicMusicFolder struct {
	Id   string `json:"id" xml:"id,attr"`
	Name string `json:"name" xml:"name,attr"`
}

type subsonicMusicFolders struct {
	MusicFolder []subsonicMusicFolder `json:"musicFolder" xml:"musicFolder"`
}

type subsonicIndex struct {
	Name   string          `json:"name" xml:"name,attr"`
	Artist []subsonicChild `json:"artist" xml:"artist"`
}

type subsonicIndexes struct {
	LastModified    int64           `json:"lastModified" xml:"lastModified,attr"`
	IgnoredArticles string          `json:"ignoredArticles" xml:"ignoredArticles,attr"`
	Index           []subsonicIndex `json:"index" xml:"index"`
}

type subsonicArtist struct {
	Id         string          `json:"id" xml:"id,attr"`
	Name       string          `json:"name" xml:"name,attr"`
	AlbumCount int             `json:"albumCount,omitempty" xml:"albumCount,attr,omitempty"`
	CoverArt   string          `json:"coverArt,omitempty" xml:"coverArt,attr,omitempty"`
	Album      []subsonicAlbum `json:"album" xml:"album"`
}

type subsonicArtists struct {
	IgnoredArticles string           `json:"ignoredArticles" xml:"ignoredArticles,attr"`
	Index           []subsonicIndex2 `json:"index" xml:"index"`
}

type subsonicIndex2 struct {
	Name   string           `json:"name" xml:"name,attr"`
	Artist []subsonicArtist `json:"artist" xml:"artist"`
}

type subsonicAlbum struct {
	Id        string          `json:"id" xml:"id,attr"`
	Name      string          `json:"name" xml:"name,attr"`
	Artist    string          `json:"artist,omitempty" xml:"artist,attr,omitempty"`
	ArtistId  string          `json:"artistId,omitempty" xml:"artistId,attr,omitempty"`
	CoverArt  string          `json:"coverArt,omitempty" xml:"coverArt,attr,omitempty"`
	SongCount int             `json:"songCount" xml:"songCount,attr"`
	Duration  int             `json:"duration" xml:"duration,attr"`
	Created   string          `json:"created" xml:"created,attr"`
	Year      int             `json:"year,omitempty" xml:"year,attr,omitempty"`
	Song      []subsonicChild `json:"song" xml:"song"`
}

type subsonicChild struct {
	Id          string `json:"id" xml:"id,attr"`
	Parent      string `json:"parent,omitempty" xml:"parent,attr,omitempty"`
	IsDir       bool   `json:"isDir" xml:"isDir,attr"`
	Title       string `json:"title" xml:"title,attr"`
	Album       string `json:"album,omitempty" xml:"album,attr,omitempty"`
	Artist      string `json:"artist,omitempty" xml:"artist,attr,omitempty"`
	Track       int    `json:"track,omitempty" xml:"track,attr,omitempty"`
	Year        int    `json:"year,omitempty" xml:"year,attr,omitempty"`
	Genre       string `json:"genre,omitempty" xml:"genre,attr,omitempty"`
	CoverArt    string `json:"coverArt,omitempty" xml:"coverArt,attr,omitempty"`
	Size        int64  `json:"size,omitempty" xml:"size,attr,omitempty"`
	ContentType string `json:"contentType,omitempty" xml:"contentType,attr,omitempty"`
	Suffix      string `json:"suffix,omitempty" xml:"suffix,attr,omitempty"`
	Duration    int    `json:"duration,omitempty" xml:"duration,attr,omitempty"`
	BitRate     int    `json:"bitRate,omitempty" xml:"bitRate,attr,omitempty"`
	Path        string `json:"path,omitempty" xml:"path,attr,omitempty"`
	AlbumId     string `json:"albumId,omitempty" xml:"albumId,attr,omitempty"`
	ArtistId    string `json:"artistId,omitempty" xml:"artistId,attr,omitempty"`
	Type        string `json:"type,omitempty" xml:"type,attr,omitempty"`
	IsVideo     bool   `json:"isVideo,omitempty" xml:"isVideo,attr,omitempty"`
	PlayCount   int    `json:"playCount,omitempty" xml:"playCount,attr,omitempty"`
	DiscNumber  int    `json:"discNumber,omitempty" xml:"discNumber,attr,omitempty"`
	Created     string `json:"created,omitempty" xml:"created,attr,omitempty"`
	Starred     string `json:"starred,omitempty" xml:"starred,attr,omitempty"`
	UserRating  int    `json:"userRating,omitempty" xml:"userRating,attr,omitempty"`
}

type subsonicAlbumList struct {
	Album []subsonicAlbum `json:"album" xml:"album"`
}

type subsonicSearchResult struct {
	Offset int              `json:"offset" xml:"offset,attr"`
	Total  int              `json:"total" xml:"total,attr"`
	Artist []subsonicArtist `json:"artist" xml:"artist"`
	Album  []subsonicAlbum  `json:"album" xml:"album"`
	Song   []subsonicChild  `json:"song" xml:"song"`
}

type subsonicSongs struct {
	Song []subsonicChild `json:"song" xml:"song"`
}

type subsonicDirectory struct {
	Id      string          `json:"id" xml:"id,attr"`
	Parent  string          `json:"parent,omitempty" xml:"parent,attr,omitempty"`
	Name    string          `json:"name" xml:"name,attr"`
	Starred string          `json:"starred,omitempty" xml:"starred,attr,omitempty"`
	Child   []subsonicChild `json:"child" xml:"child"`
}

type subsonicPlaylists struct {
	Playlist []subsonicPlaylist `json:"playlist" xml:"playlist"`
}

type subsonicPlaylist struct {
	Id        string          `json:"id" xml:"id,attr"`
	Name      string          `json:"name" xml:"name,attr"`
	Comment   string          `json:"comment,omitempty" xml:"comment,attr,omitempty"`
	Owner     string          `json:"owner,omitempty" xml:"owner,attr,omitempty"`
	Public    bool            `json:"public" xml:"public,attr"`
	SongCount int             `json:"songCount" xml:"songCount,attr"`
	Duration  int             `json:"duration" xml:"duration,attr"`
	Created   string          `json:"created" xml:"created,attr"`
	Entry     []subsonicChild `json:"entry" xml:"entry"`
}

type subsonicLyrics struct {
	Artist string `json:"artist,omitempty" xml:"artist,attr,omitempty"`
	Title  string `json:"title,omitempty" xml:"title,attr,omitempty"`
	Value  string `json:"value" xml:",chardata"`
}

type subsonicLyricsList struct {
	StructuredLyrics []subsonicStructuredLyrics `json:"structuredLyrics" xml:"structuredLyrics"`
}

type subsonicStructuredLyrics struct {
	DisplayArtist string              `json:"displayArtist,omitempty" xml:"displayArtist,attr,omitempty"`
	DisplayTitle  string              `json:"displayTitle,omitempty" xml:"displayTitle,attr,omitempty"`
	Lang          string              `json:"lang" xml:"lang,attr"`
	Synced        bool                `json:"synced" xml:"synced,attr"`
	Line          []subsonicLyricLine `json:"line" xml:"line"`
}

type subsonicLyricLine struct {
	Start int64  `json:"start" xml:"start,attr"`
	Value string `json:"value" xml:"value,attr"`
}

type subsonicStarred struct {
	Artist []subsonicArtist `json:"artist" xml:"artist"`
	Album  []subsonicAlbum  `json:"album" xml:"album"`
	Song   []subsonicChild  `json:"song" xml:"song"`
}

type subsonicGenre struct {
	Value      string `json:"value" xml:",chardata"`
	SongCount  int    `json:"songCount" xml:"songCount,attr"`
	AlbumCount int    `json:"albumCount" xml:"albumCount,attr"`
}

type subsonicGenres struct {
	Genre []subsonicGenre `json:"genre" xml:"genre"`
}

type subsonicScanStatus struct {
	Scanning bool  `json:"scanning" xml:"scanning,attr"`
	Count    int64 `json:"count" xml:"count,attr"`
}

type subsonicUser struct {
	Username          string `json:"username" xml:"username,attr"`
	Email             string `json:"email,omitempty" xml:"email,attr,omitempty"`
	ScrobblingEnabled bool   `json:"scrobblingEnabled" xml:"scrobblingEnabled,attr"`
	AdminRole         bool   `json:"adminRole" xml:"adminRole,attr"`
	DownloadRole      bool   `json:"downloadRole" xml:"downloadRole,attr"`
	PlaylistRole      bool   `json:"playlistRole" xml:"playlistRole,attr"`
	StreamRole        bool   `json:"streamRole" xml:"streamRole,attr"`
	CoverArtRole      bool   `json:"coverArtRole" xml:"coverArtRole,attr"`
}

type subsonicUsers struct {
	User []subsonicUser `json:"user" xml:"user"`
}

type subsonicExtension struct {
	Name     string `json:"name" xml:"name,attr"`
	Versions []int  `json:"versions" xml:"versions"`
}

type subsonicExtensions struct {
	Extension []subsonicExtension `json:"openSubsonicExtension" xml:"openSubsonicExtension"`
}

type subsonicNowPlaying struct {
	Entry []subsonicChild `json:"entry" xml:"entry"`
}

type subsonicPlayQueue struct {
	Current  string          `json:"current,omitempty" xml:"current,attr,omitempty"`
	Position int64           `json:"position,omitempty" xml:"position,attr,omitempty"`
	Username string          `json:"username,omitempty" xml:"username,attr,omitempty"`
	Changed  string          `json:"changed,omitempty" xml:"changed,attr,omitempty"`
	Entry    []subsonicChild `json:"entry" xml:"entry"`
}

type subsonicArtistInfo struct {
	Biography     string `json:"biography,omitempty" xml:"biography,omitempty"`
	MusicBrainzId string `json:"musicBrainzId,omitempty" xml:"musicBrainzId,omitempty"`
}

type subsonicAlbumInfo struct {
	Notes string `json:"notes,omitempty" xml:"notes,omitempty"`
}

type subsonicBookmarks struct {
	Bookmark []any `json:"bookmark" xml:"bookmark"`
}

// ==================== 分发 ====================

// SubsonicAPI OpenSubsonic API 入口（/@rest/*）
func SubsonicAPI(c *gin.Context) {
	if !MediaSubsonicEnabled() {
		c.AbortWithStatus(404)
		return
	}
	name := strings.Trim(c.Param("path"), "/")
	// 兼容接入地址已带 /@rest、客户端又自动追加 /rest 的情况
	if first, rest, found := strings.Cut(name, "/"); found && strings.EqualFold(first, "rest") {
		name = rest
	}
	name = strings.TrimSuffix(name, ".view")
	name = strings.ToLower(name)
	if name == "" {
		subError(c, 0, "missing method")
		return
	}

	// 免认证端点
	if name == "getopensubsonicextensions" {
		subRespond(c, &subsonicResponse{OpenSubsonicExtensions: &subsonicExtensions{
			Extension: []subsonicExtension{
				{Name: "apiKeyAuthentication", Versions: []int{1}},
				{Name: "formPost", Versions: []int{1}},
				{Name: "songLyrics", Versions: []int{1}},
			},
		}})
		return
	}

	user, _, ok := subAuth(c)
	if !ok {
		// 锁定判定同样要看用户名维度，否则被锁的账号只会收到「密码错误」
		if mediaAuthLockedFor(c, subParam(c, "u")) {
			subError(c, 40, "Too many failed attempts, please try again later")
			return
		}
		subError(c, 40, "Wrong username or password; use an app key as the password or enable password auth")
		return
	}

	switch name {
	case "ping":
		subRespond(c, &subsonicResponse{})
	case "getlicense":
		subRespond(c, &subsonicResponse{License: &subsonicLicense{Valid: true}})
	case "tokeninfo":
		subRespond(c, &subsonicResponse{User: subUserOf(user)})
	case "getuser":
		subRespond(c, &subsonicResponse{User: subUserOf(user)})
	case "getusers":
		if !user.IsAdmin() {
			subError(c, 50, "not authorized")
			return
		}
		users, _, err := op.GetUsers(1, -1)
		if err != nil {
			subError(c, 0, err.Error())
			return
		}
		out := &subsonicUsers{}
		for i := range users {
			if users[i].IsGuest() {
				continue
			}
			out.User = append(out.User, *subUserOf(&users[i]))
		}
		subRespond(c, &subsonicResponse{Users: out})
	case "getmusicfolders":
		subRespond(c, &subsonicResponse{MusicFolders: &subsonicMusicFolders{
			MusicFolder: []subsonicMusicFolder{{Id: "1", Name: "Music"}},
		}})
	case "getindexes":
		subGetIndexes(c, user)
	case "getartists":
		subGetArtists(c, user)
	case "getartist":
		subGetArtist(c, user)
	case "getalbum":
		subGetAlbum(c, user)
	case "getsong":
		subGetSong(c, user)
	case "getmusicdirectory":
		subGetMusicDirectory(c, user)
	case "getalbumlist", "getalbumlist2":
		subGetAlbumList(c, user, name == "getalbumlist")
	case "getrandomsongs":
		subGetRandomSongs(c, user)
	case "getsongsbygenre":
		subGetSongsByGenre(c, user)
	case "search2", "search3":
		subSearch(c, user, name == "search2")
	case "getstarred", "getstarred2":
		subGetStarred(c, user, name == "getstarred")
	case "stream", "download":
		subStream(c, user)
	case "getcoverart":
		subGetCoverArt(c, user)
	case "getlyrics":
		subGetLyrics(c, user)
	case "getlyricsbysongid":
		subGetLyricsBySongID(c, user)
	case "star", "unstar":
		subStar(c, user, name == "star")
	case "setrating":
		subSetRating(c, user)
	case "scrobble":
		subScrobble(c, user)
	case "getnowplaying":
		subRespond(c, &subsonicResponse{NowPlaying: &subsonicNowPlaying{Entry: subNowPlayingEntries()}})
	case "getplaylists":
		subGetPlaylists(c, user)
	case "getplaylist":
		subGetPlaylist(c, user)
	case "createplaylist":
		subCreatePlaylist(c, user)
	case "updateplaylist":
		subUpdatePlaylist(c, user)
	case "deleteplaylist":
		subDeletePlaylist(c, user)
	case "getscanstatus":
		subRespond(c, &subsonicResponse{ScanStatus: &subsonicScanStatus{Scanning: false, Count: subSongCount(user)}})
	case "startscan":
		if !user.IsAdmin() {
			subError(c, 50, "not authorized")
			return
		}
		subStartScan(c)
	case "saveplayqueue":
		subSavePlayQueue(c, user)
	case "getplayqueue":
		subRespond(c, &subsonicResponse{PlayQueue: subLoadPlayQueue(user)})
	case "getartistinfo2":
		subRespond(c, &subsonicResponse{ArtistInfo2: &subsonicArtistInfo{}})
	case "getalbuminfo", "getalbuminfo2":
		subRespond(c, &subsonicResponse{AlbumInfo: &subsonicAlbumInfo{}})
	case "gettopsongs", "getsimilarsongs2":
		subRespond(c, &subsonicResponse{TopSongs: &subsonicSongs{}})
	case "getbookmarks":
		subRespond(c, &subsonicResponse{Bookmarks: &subsonicBookmarks{}})
	case "getgenres":
		subGetGenres(c, user)
	case "getavatars":
		c.Status(404)
	// 明确不支持
	case "getpodcasts", "getnewestpodcasts", "refreshpodcasts", "createpodcastchannel",
		"deletepodcastchannel", "deletepodcastepisode", "downloadpodcastepisode",
		"createuser", "updateuser", "deleteuser", "changepassword",
		"getshares", "createshare", "updateshare", "deleteshare", "jukeboxcontrol":
		c.String(501, "This endpoint is not implemented, but may be in future releases")
	case "search", "getchatmessages", "addchatmessage", "getvideos", "getvideoinfo", "getcaptions", "hls":
		c.String(410, "This endpoint will not be implemented")
	default:
		subError(c, 0, "unknown method: "+name)
	}
}

// ==================== 参数 / 认证 / 响应 ====================

func subParam(c *gin.Context, key string) string {
	if v := c.Query(key); v != "" {
		return v
	}
	if c.Request.Method == "POST" {
		_ = c.Request.ParseForm()
		if v := c.Request.PostForm.Get(key); v != "" {
			return v
		}
	}
	return ""
}

func subIntParam(c *gin.Context, key string, def int) int {
	v := subParam(c, key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// subAuth 支持 apiKey / u+t+s / u+p（默认关闭）认证，并对失败次数做 IP + 用户名双维度限流
func subAuth(c *gin.Context) (*model.User, *model.MediaAppKey, bool) {
	username := strings.TrimSpace(subParam(c, "u"))
	if mediaAuthLockedFor(c, username) {
		return nil, nil, false
	}
	u, k, ok := subAuthInner(c)
	if ok {
		// 用真实用户名清除计数：apiKey 认证时 u 参数是空的
		mediaAuthSucceededFor(c, u.Username)
	} else if subHasCredential(c) {
		// 只有「确实带了凭据但校验失败」才计数；
		// 完全裸请求（部分客户端启动时会先发一次不带参数的探测）不计入失败次数。
		mediaAuthFailedFor(c, username)
	}
	return u, k, ok
}

// subHasCredential 判断请求是否真的携带了认证凭据
func subHasCredential(c *gin.Context) bool {
	return subParam(c, "apiKey") != "" || subParam(c, "u") != "" ||
		subParam(c, "t") != "" || subParam(c, "p") != ""
}

func subAuthInner(c *gin.Context) (*model.User, *model.MediaAppKey, bool) {
	// OpenSubsonic apiKey 扩展：直接把应用密钥放在 apiKey 参数里
	if key := strings.TrimSpace(subParam(c, "apiKey")); key != "" {
		if u, k, ok := subAuthByAppKey(key); ok {
			return u, k, true
		}
		return nil, nil, false
	}
	username := strings.TrimSpace(subParam(c, "u"))
	password := decodeSubsonicPassword(subParam(c, "p"))
	// 用户名留空、只填了应用密钥（部分客户端只允许填一个"密码"字段）
	if username == "" {
		if password != "" {
			if u, k, ok := subAuthByAppKey(password); ok {
				return u, k, true
			}
		}
		return nil, nil, false
	}
	user, err := op.GetUserByName(username)
	if err != nil || user.Disabled || user.IsGuest() {
		return nil, nil, false
	}
	// t+s：用应用密钥的明文做 md5(密码+salt) 校验（Subsonic token 认证）
	if salt := subParam(c, "s"); salt != "" {
		if token := subParam(c, "t"); token != "" {
			keys, _ := db.ListMediaAppKeys(user.ID)
			for i := range keys {
				plain, err := media.DecryptAppKey(keys[i].Secret)
				if err != nil {
					continue
				}
				if media.MatchSubsonicToken(plain, salt, token) {
					db.TouchMediaAppKey(keys[i].ID)
					return user, &keys[i], true
				}
			}
			// 不直接返回：个别客户端会同时带上 p，继续按明文校验
		}
	}
	// p：OpenList 密码或应用密钥明文
	if password != "" {
		// 使用主账号密码认证会把登录密码暴露给第三方客户端，必须由管理员显式开启
		if setting.GetBool(conf.MediaSubsonicAllowPwd) && user.OtpSecret == "" &&
			user.ValidateRawPassword(password) == nil {
			return user, nil, true
		}
		if u, k, ok := subAuthByAppKey(password); ok && u.ID == user.ID {
			return u, k, true
		}
	}
	return nil, nil, false
}

// decodeSubsonicPassword 解析 p 参数，兼容 Subsonic 的 enc:<hex> 编码
func decodeSubsonicPassword(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "enc:") {
		if raw, err := hex.DecodeString(strings.TrimPrefix(p, "enc:")); err == nil {
			return string(raw)
		}
	}
	return p
}

func subAuthByAppKey(key string) (*model.User, *model.MediaAppKey, bool) {
	return LookUpAppKeyUser(key)
}

// subsonicServerVersion 返回给 Subsonic 客户端的服务器版本（仅用于展示）
func subsonicServerVersion() string {
	if conf.Version == "" {
		return "OpenList"
	}
	return conf.Version
}

func subRespond(c *gin.Context, resp *subsonicResponse) {
	resp.Status = "ok"
	resp.Version = subsonicAPIVersion
	resp.Type = "OpenList"
	resp.ServerVersion = subsonicServerVersion()
	resp.OpenSubsonic = true
	format := strings.ToLower(subParam(c, "f"))
	switch format {
	case "json":
		c.JSON(200, gin.H{"subsonic-response": resp})
	case "jsonp":
		callback := subParam(c, "callback")
		if !validJsonpCallback(callback) {
			c.JSON(200, gin.H{"subsonic-response": &subsonicResponse{
				Status: "failed", Version: subsonicAPIVersion, Type: "OpenList",
				ServerVersion: subsonicServerVersion(), OpenSubsonic: true,
				Error: &subsonicError{Code: 0, Message: "invalid callback parameter"},
			}})
			return
		}
		body, _ := json.Marshal(gin.H{"subsonic-response": resp})
		c.Header("Content-Type", "application/javascript; charset=utf-8")
		c.String(200, "%s(%s)", callback, string(body))
	default:
		c.XML(200, resp)
	}
}

func subError(c *gin.Context, code int, message string) {
	resp := &subsonicResponse{
		Status:        "failed",
		Version:       subsonicAPIVersion,
		Type:          "OpenList",
		ServerVersion: subsonicServerVersion(),
		OpenSubsonic:  true,
		Error:         &subsonicError{Code: code, Message: message},
	}
	format := strings.ToLower(subParam(c, "f"))
	switch format {
	case "json":
		c.JSON(200, gin.H{"subsonic-response": resp})
	case "jsonp":
		body, _ := json.Marshal(gin.H{"subsonic-response": resp})
		c.String(200, "%s(%s)", subParam(c, "callback"), string(body))
	default:
		c.XML(200, resp)
	}
}

func validJsonpCallback(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == '$' {
			continue
		}
		if i > 0 && (r >= '0' && r <= '9' || r == '.') {
			continue
		}
		return false
	}
	return true
}

// ==================== 数据映射 ====================

func subUserOf(u *model.User) *subsonicUser {
	return &subsonicUser{
		Username:          u.Username,
		ScrobblingEnabled: true,
		AdminRole:         u.IsAdmin(),
		DownloadRole:      true,
		PlaylistRole:      true,
		StreamRole:        true,
		CoverArtRole:      true,
	}
}

func (l *embyLibrary) subChild(it *model.MediaItem) subsonicChild {
	title := it.ScrapedName
	if title == "" {
		title = strings.TrimSuffix(it.FileName, stdpath.Ext(it.FileName))
	}
	album := it.AlbumName
	if album == "" {
		album = "Unknown Album"
	}
	artist := albumArtistOf(it)
	image := fmt.Sprintf("al-%s", albumKeyOf(it))
	year := 0
	if len(it.ReleaseDate) >= 4 {
		year, _ = strconv.Atoi(it.ReleaseDate[:4])
	}
	userData := l.userDataOf(it.ID, 0)
	c := subsonicChild{
		Id:          fmt.Sprintf("so-%d", it.ID),
		Parent:      image,
		IsDir:       false,
		Title:       title,
		Album:       album,
		Artist:      artist,
		Track:       it.TrackNumber,
		Year:        year,
		Genre:       it.Genre,
		CoverArt:    image,
		Size:        it.FileSize,
		ContentType: it.MimeType,
		Suffix:      strings.TrimPrefix(strings.ToLower(stdpath.Ext(it.FileName)), "."),
		Duration:    it.Duration,
		Path:        MediaItemFilePath(it),
		AlbumId:     image,
		ArtistId:    "ar-" + artistKeyOf(it),
		Type:        "music",
		PlayCount:   userData.PlayCount,
		Created:     it.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		UserRating:  userDataRating(it.ID, l),
	}
	if userData.IsFavorite {
		c.Starred = it.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return c
}

func userDataRating(itemID uint, l *embyLibrary) int {
	if l == nil {
		return 0
	}
	if d, ok := l.userData[[2]uint{itemID, 0}]; ok {
		return d.Rating
	}
	return 0
}

func (l *embyLibrary) subAlbum(al *embyAlbum) subsonicAlbum {
	duration := 0
	for i := range al.Items {
		duration += al.Items[i].Duration
	}
	created := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if len(al.Items) > 0 {
		created = al.Items[0].CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return subsonicAlbum{
		Id:        "al-" + al.Key,
		Name:      al.Name,
		Artist:    al.Artist,
		ArtistId:  "ar-" + artistKeyOf(&al.Items[0]),
		CoverArt:  "al-" + al.Key,
		SongCount: len(al.Items),
		Duration:  duration,
		Created:   created,
		Year:      al.Year,
	}
}

func (l *embyLibrary) subAlbumWithSongs(al *embyAlbum) subsonicAlbum {
	out := l.subAlbum(al)
	for i := range al.Items {
		out.Song = append(out.Song, l.subChild(&al.Items[i]))
	}
	return out
}

func (l *embyLibrary) subArtist(key, name string) subsonicArtist {
	out := subsonicArtist{Id: "ar-" + key, Name: name}
	for _, al := range l.albums() {
		if artistKeyOf(&al.Items[0]) != key {
			continue
		}
		out.Album = append(out.Album, l.subAlbum(al))
	}
	out.AlbumCount = len(out.Album)
	if len(out.Album) > 0 {
		out.CoverArt = out.Album[0].CoverArt
	}
	return out
}

func (l *embyLibrary) subArtists() []subsonicArtist {
	seen := map[string]bool{}
	var out []subsonicArtist
	for i := range l.music {
		key := artistKeyOf(&l.music[i])
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, l.subArtist(key, albumArtistOf(&l.music[i])))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}
