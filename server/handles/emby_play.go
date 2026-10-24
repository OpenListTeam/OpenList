package handles

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	stdpath "path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/media"
	"github.com/OpenListTeam/OpenList/v4/internal/media/imgmime"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// embyPlayTarget 播放目标（条目或某一集）
type embyPlayTarget struct {
	Item      *model.MediaItem
	Episode   *model.EpisodeInfo
	RawPath   string
	Container string
	Size      int64
	Name      string
	Duration  int
	ItemID    uint
	EpisodeNo int
}

func (l *embyLibrary) playTarget(id string) (*embyPlayTarget, bool) {
	id = l.origID(id)
	switch {
	case strings.HasPrefix(id, "mv-"):
		n, err := strconv.ParseUint(strings.TrimPrefix(id, "mv-"), 10, 64)
		if err != nil {
			return nil, false
		}
		for i := range l.videos {
			if l.videos[i].ID == uint(n) {
				it := &l.videos[i]
				return &embyPlayTarget{
					Item: it, RawPath: MediaItemFilePath(it), Container: embyExt(it.FileName),
					Size: it.FileSize, Name: it.FileName, Duration: it.Duration, ItemID: it.ID,
				}, true
			}
		}
	case strings.HasPrefix(id, "so-"):
		it, ok := l.songByID(id)
		if !ok {
			return nil, false
		}
		return &embyPlayTarget{
			Item: it, RawPath: MediaItemFilePath(it), Container: embyExt(it.FileName),
			Size: it.FileSize, Name: it.FileName, Duration: it.Duration, ItemID: it.ID,
		}, true
	case strings.HasPrefix(id, "ep-"):
		ep := l.findEpisode(id)
		if ep == nil {
			return nil, false
		}
		raw := MediaItemFilePath(ep.Item)
		if ep.Episode.FileName != "" {
			raw = stdpath.Join(mediaFolderOf(ep.Item), ep.Item.FileName, ep.Episode.FileName)
		}
		return &embyPlayTarget{
			Item: ep.Item, Episode: &ep.Episode, RawPath: raw, Container: embyExt(raw),
			Size: ep.Item.FileSize, Name: ep.Episode.FileName, Duration: ep.Item.Duration,
			ItemID: ep.Item.ID, EpisodeNo: ep.Index,
		}, true
	}
	return nil, false
}

// embyPlaybackInfo 返回直连播放信息
func embyPlaybackInfo(c *gin.Context, lib *embyLibrary, id string) {
	target, ok := lib.playTarget(id)
	if !ok {
		embyNotFound(c)
		return
	}
	api := common_ApiBase(c)
	isAudio := target.Item.MediaType == model.MediaTypeMusic
	kind, streamType := "Videos", "Video"
	if isAudio {
		kind, streamType = "Audio", "Audio"
	}
	// 流地址必须自带 api_key：播放器取流时不会再附加请求头，
	// 缺失会得到 401，客户端表现为"Playback of this content is currently restricted"
	q := url.Values{}
	q.Set("Static", "true")
	q.Set("MediaSourceId", id)
	if tok := embyRequestToken(c); tok != "" {
		q.Set("api_key", tok)
	}
	streamURL := fmt.Sprintf("%s/@emby/%s/%s/stream.%s?%s", api, kind, id, target.Container, q.Encode())
	if target.Container == "" {
		streamURL = fmt.Sprintf("%s/@emby/%s/%s/stream?%s", api, kind, id, q.Encode())
	}
	// 必须是 Http：客户端看到 File 会当成"服务器本地文件"，
	// 转而调用原生 LocalItemRepository 解析本地路径，必然失败（Playback Error）
	streams := []embyMediaStream{{Type: streamType, Index: 0, IsDefault: true}}
	var subtitleIndex *int
	if isAudio && target.Item.Lyrics != "" {
		// 歌词以"外挂字幕轨"的形式提供给客户端（Emby Theater 的歌词面板
		// 只遍历 Subtitle 流，并请求 Subtitles/{index}/Stream.js）
		lyricsIndex := len(streams)
		streams = append(streams, embyLyricsMediaStream(lyricsIndex))
		subtitleIndex = &lyricsIndex
	}
	source := embyMediaSource{
		Protocol:                   "Http",
		Id:                         id,
		ItemId:                     id,
		// Emby Theater 在本地播放时会把 Path 直接交给 mpv；
		// 填文件系统路径会让 mpv 去开本机不存在的文件（一直转圈/Playback Error），
		// 这里填可播放的 HTTP 流地址。
		Path:                       streamURL,
		Type:                       "Default",
		Container:                  target.Container,
		Name:                       target.Name,
		Size:                       target.Size,
		ETag:                       embyHash(fmt.Sprintf("%s|%d", target.RawPath, target.Size)),
		RunTimeTicks:               embyTicks(target.Duration),
		SupportsDirectPlay:         true,
		SupportsDirectStream:       true,
		SupportsTranscoding:        false,
		SupportsProbing:            false,
		MediaStreams:               streams,
		MediaAttachments:           []any{},
		Formats:                    []string{},
		RequiredHttpHeaders:        map[string]string{},
		DirectStreamUrl:            streamURL,
		AddApiKeyToDirectStreamUrl: false,
		DefaultSubtitleStreamIndex: subtitleIndex,
	}
	c.JSON(200, gin.H{
		"MediaSources":  []embyMediaSource{source},
		"PlaySessionId": fmt.Sprintf("openlist-%d-%d", target.ItemID, target.EpisodeNo),
	})
}

// embyStream 302 到签名直链
func embyStream(c *gin.Context, user *model.User, id string, isAudio bool) {
	lib := loadEmbyLibrary(user)
	target, ok := lib.playTarget(id)
	if !ok {
		embyNotFound(c)
		return
	}
	url := SignedStreamURL(c, target.RawPath)
	c.Redirect(http.StatusFound, url)
}

// ==================== 图片 ====================

var dataURIPrefix = "data:image/"

// embyImage 返回条目封面
func embyImage(c *gin.Context, id string, segs []string, allowAnon bool) {
	var lib *embyLibrary
	if user, ok := embyAuth(c); ok {
		lib = loadEmbyLibrary(user)
	} else if allowAnon {
		// Emby 客户端的封面 URL 不带 token，只能允许匿名访问；
		// 但必须按访客身份做权限过滤，否则等于把整个媒体库（含 hidden、
		// base_path 之外、meta 受保护的条目）的封面暴露给未登录用户。
		// 访客被禁用时直接拒绝，不能退化成「全库可见」。
		guest, err := op.GetGuest()
		if err != nil || guest.Disabled {
			embyUnauthorized(c)
			return
		}
		lib = loadEmbyLibrary(guest)
	} else {
		embyUnauthorized(c)
		return
	}
	// 客户端回传的是 GUID，这里先还原成内部 ID 再做前缀判断。
	// 注意不能先走 resolveItem：匿名库的 user 为 nil，剧集条目会被权限校验判为不可见。
	id = lib.origID(id)
	var mediaItem *model.MediaItem
	switch {
	case strings.HasPrefix(id, "mv-"):
		n, _ := strconv.ParseUint(strings.TrimPrefix(id, "mv-"), 10, 64)
		for i := range lib.videos {
			if lib.videos[i].ID == uint(n) {
				mediaItem = &lib.videos[i]
			}
		}
	case strings.HasPrefix(id, "so-"):
		mediaItem, _ = lib.songByID(id)
	case strings.HasPrefix(id, "ep-"):
		// 图片 URL 不带 token，匿名库的 user 为 nil；
		// findEpisode 会做用户权限校验并直接失败，这里只按库内条目定位封面来源
		if it, ok := lib.episodeSourceItem(id); ok {
			mediaItem = it
		}
	case strings.HasPrefix(id, "sr-"):
		if s := lib.seriesByKey(strings.TrimPrefix(id, "sr-")); s != nil && len(s.Items) > 0 {
			mediaItem = &s.Items[0]
		}
	case strings.HasPrefix(id, "sn-"):
		rest := strings.TrimPrefix(id, "sn-")
		if idx := strings.LastIndex(rest, "-"); idx > 0 {
			if s := lib.seriesByKey(rest[:idx]); s != nil && len(s.Items) > 0 {
				mediaItem = &s.Items[0]
			}
		}
	case strings.HasPrefix(id, "al-"):
		if al := lib.albumByKey(strings.TrimPrefix(id, "al-")); al != nil && len(al.Items) > 0 {
			mediaItem = &al.Items[0]
		}
	case strings.HasPrefix(id, "ar-"):
		key := strings.TrimPrefix(id, "ar-")
		for i := range lib.music {
			if artistKeyOf(&lib.music[i]) == key {
				mediaItem = &lib.music[i]
				break
			}
		}
	}
	if mediaItem == nil {
		c.Status(404)
		return
	}
	serveEmbyCover(c, mediaItem)
}

// episodeSourceItem 按 ep-<itemID>-<num> 在库内定位剧集所属条目，不做权限校验。
// 仅用于封面这类匿名可访问的资源，播放与条目详情仍走 findEpisode 的权限校验。
func (l *embyLibrary) episodeSourceItem(id string) (*model.MediaItem, bool) {
	parts := strings.Split(strings.TrimPrefix(id, "ep-"), "-")
	if len(parts) != 2 {
		return nil, false
	}
	n, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return nil, false
	}
	for i := range l.videos {
		if l.videos[i].ID == uint(n) {
			return &l.videos[i], true
		}
	}
	return nil, false
}

func serveEmbyCover(c *gin.Context, item *model.MediaItem) {
	cover := strings.TrimSpace(item.Cover)
	if cover == "" {
		c.Status(404)
		return
	}
	if strings.HasPrefix(cover, dataURIPrefix) {
		idx := strings.Index(cover, ";base64,")
		if idx < 0 {
			c.Status(404)
			return
		}
		data, err := base64.StdEncoding.DecodeString(cover[idx+8:])
		if err != nil {
			c.Status(404)
			return
		}
		// 数据库里的 data URI 声明的 MIME 不可信（历史数据可能来自内嵌标签/远端响应），
		// 响应头只使用按魔数嗅探出的四种栅格类型，SVG/HTML 一律拒绝。
		mime := imgmime.Sniff(data)
		if mime == "" {
			c.Status(404)
			return
		}
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Content-Disposition", `inline; filename="cover"`)
		c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
		c.Header("Cache-Control", "private, max-age=86400")
		c.Data(200, mime, data)
		return
	}
	if strings.HasPrefix(cover, "http://") || strings.HasPrefix(cover, "https://") {
		c.Redirect(302, cover)
		return
	}
	// 本地缩略图路径
	c.Redirect(302, SignedStreamURL(c, cover))
}

// ==================== 歌词 ====================

var lrcLineRe = regexp.MustCompile(`\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\](.*)`)

func parseLrc(content string) []gin.H {
	var out []gin.H
	for _, line := range strings.Split(content, "\n") {
		m := lrcLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) == 0 {
			continue
		}
		min, _ := strconv.Atoi(m[1])
		sec, _ := strconv.Atoi(m[2])
		ms := 0
		if m[3] != "" {
			frac := m[3]
			for len(frac) < 3 {
				frac += "0"
			}
			ms, _ = strconv.Atoi(frac[:3])
		}
		text := strings.TrimSpace(m[4])
		out = append(out, gin.H{
			"Text":  text,
			"Start": int64(min*60000 + sec*1000 + ms),
		})
	}
	return out
}

func embyLyrics(c *gin.Context, user *model.User, id string) {
	lib := loadEmbyLibrary(user)
	item, ok := lib.songByID(id)
	if !ok || item.Lyrics == "" {
		c.Status(404)
		return
	}
	lines := parseLrc(item.Lyrics)
	c.JSON(200, gin.H{"Lyrics": lines, "IsSynced": len(lines) > 0})
}

// embyLyricsMediaStream 构造歌词字幕轨（Emby 用外挂字幕承载歌词）。
func embyLyricsMediaStream(index int) embyMediaStream {
	return embyMediaStream{
		Type:                   "Subtitle",
		Index:                  index,
		Codec:                  "lrc",
		Language:               "und",
		DisplayTitle:           "Lyrics",
		IsExternal:             true,
		IsTextSubtitleStream:   true,
		SupportsExternalStream: true,
		DeliveryMethod:         "External",
		DeliveryUrl:            "",
	}
}

// embyLyricsTrackEvents 把 LRC 转成客户端歌词面板使用的 TrackEvent 列表。
// 客户端按 StartPositionTicks 与当前播放位置比较（1ms = 10000 ticks）。
// 没有时间轴的纯文本歌词会按行均分到歌曲时长，至少能顺序展示。
func embyLyricsTrackEvents(content string, durationSec int) []gin.H {
	lines := parseLrc(content)
	if len(lines) == 0 {
		texts := make([]string, 0, 32)
		for _, l := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
			if t := strings.TrimSpace(l); t != "" {
				texts = append(texts, t)
			}
		}
		if len(texts) > 0 {
			total := int64(durationSec) * 1000
			if total <= 0 {
				total = int64(len(texts)) * 5000
			}
			step := total / int64(len(texts))
			for i, t := range texts {
				lines = append(lines, gin.H{"Text": t, "Start": int64(i) * step})
			}
		}
	}
	events := make([]gin.H, 0, len(lines))
	for _, l := range lines {
		ms, _ := l["Start"].(int64)
		events = append(events, gin.H{
			"StartPositionTicks": ms * 10000,
			"Text":               l["Text"],
		})
	}
	return events
}

// embySubtitleStream 处理 /Items/{id}/{mediaSourceId}/Subtitles/{index}/Stream.js
// Emby Theater 用它拉取歌词行（JSON 形式的 TrackEvents）
func embySubtitleStream(c *gin.Context, user *model.User, id string) {
	lib := loadEmbyLibrary(user)
	item, ok := lib.songByID(lib.origID(id))
	if !ok || item.Lyrics == "" {
		c.JSON(200, gin.H{"TrackEvents": []any{}})
		return
	}
	events := embyLyricsTrackEvents(item.Lyrics, item.Duration)
	c.JSON(200, gin.H{"TrackEvents": events, "TotalRecordCount": len(events)})
}

// ==================== 播放会话 / 用户状态 ====================

type embyPlaybackBody struct {
	ItemId        string `json:"ItemId"`
	ItemID        string `json:"ItemID"`
	PositionTicks int64  `json:"PositionTicks"`
	PlayMethod    string `json:"PlayMethod"`
	EventName     string `json:"EventName"`
	MediaSourceId string `json:"MediaSourceId"`
}

func (b embyPlaybackBody) itemID() string {
	if b.ItemId != "" {
		return b.ItemId
	}
	return b.ItemID
}

func embySessionPlaying(c *gin.Context, user *model.User, action string) {
	var body embyPlaybackBody
	_ = c.ShouldBindJSON(&body)
	if action == "ping" {
		c.Status(204)
		return
	}
	id := body.itemID()
	if id == "" {
		id = c.Query("ItemId")
	}
	if id == "" {
		c.Status(204)
		return
	}
	stopped := action == "stopped" || action == "stop"
	lib := loadEmbyLibrary(user)
	target, ok := lib.playTarget(id)
	if !ok {
		c.Status(204)
		return
	}
	position := body.PositionTicks
	if position == 0 {
		if v, err := strconv.ParseInt(c.DefaultQuery("PositionTicks", "0"), 10, 64); err == nil {
			position = v
		}
	}
	played := false
	if stopped && target.Duration > 0 {
		ratio := float64(position) / float64(embyTicks(target.Duration))
		played = ratio >= 0.9
	}
	applyProgress(user, target, position, played, stopped)
	c.Status(204)
}

func embySaveProgress(c *gin.Context, user *model.User, id string, start bool) {
	var body embyPlaybackBody
	_ = c.ShouldBindJSON(&body)
	lib := loadEmbyLibrary(user)
	target, ok := lib.playTarget(id)
	if !ok {
		c.Status(204)
		return
	}
	position := body.PositionTicks
	if position == 0 {
		position, _ = strconv.ParseInt(c.DefaultQuery("PositionTicks", "0"), 10, 64)
	}
	played := false
	stopped := c.Request.Method == "DELETE"
	if stopped && target.Duration > 0 {
		played = float64(position)/float64(embyTicks(target.Duration)) >= 0.9
	}
	applyProgress(user, target, position, played, stopped)
	c.Status(204)
}

func applyProgress(user *model.User, target *embyPlayTarget, position int64, played bool, stopped bool) {
	if user == nil || target == nil {
		return
	}
	d, err := db.GetMediaUserData(user.ID, target.ItemID, target.EpisodeNo)
	if err != nil || d == nil {
		d = &model.MediaUserData{UserID: user.ID, ItemID: target.ItemID, EpisodeIndex: target.EpisodeNo}
	}
	d.PositionTicks = position
	if played {
		d.Played = true
		d.PositionTicks = 0
		d.PlayCount++
	}
	now := time.Now()
	d.LastPlayedAt = &now
	_ = db.SaveMediaUserData(d)
}

func embySetPlayed(c *gin.Context, user *model.User, id string, played bool) {
	lib := loadEmbyLibrary(user)
	id = lib.origID(id)
	target, ok := lib.playTarget(id)
	if !ok {
		// 剧集/季整体标记
		if strings.HasPrefix(id, "sr-") || strings.HasPrefix(id, "sn-") {
			embyMarkContainerPlayed(c, user, lib, id, played)
			return
		}
		embyNotFound(c)
		return
	}
	d, err := db.GetMediaUserData(user.ID, target.ItemID, target.EpisodeNo)
	if err != nil || d == nil {
		d = &model.MediaUserData{UserID: user.ID, ItemID: target.ItemID, EpisodeIndex: target.EpisodeNo}
	}
	d.Played = played
	if played {
		d.PositionTicks = 0
		d.PlayCount++
	} else {
		d.PlayCount = 0
	}
	now := time.Now()
	d.LastPlayedAt = &now
	_ = db.SaveMediaUserData(d)
	c.JSON(200, gin.H{
		"PlaybackPositionTicks": d.PositionTicks,
		"PlayCount":             d.PlayCount,
		"Played":                d.Played,
		"IsFavorite":            d.IsFavorite,
	})
}

func embyMarkContainerPlayed(c *gin.Context, user *model.User, lib *embyLibrary, id string, played bool) {
	var targets []*embyPlayTarget
	if strings.HasPrefix(id, "sr-") {
		key := strings.TrimPrefix(id, "sr-")
		for _, ep := range lib.episodesOf(key, 0) {
			if t, ok := lib.playTarget(ep.ID); ok {
				targets = append(targets, t)
			}
		}
	} else {
		rest := strings.TrimPrefix(id, "sn-")
		idx := strings.LastIndex(rest, "-")
		if idx > 0 {
			key := rest[:idx]
			season, _ := strconv.Atoi(rest[idx+1:])
			for _, ep := range lib.episodesOf(key, season) {
				if t, ok := lib.playTarget(ep.ID); ok {
					targets = append(targets, t)
				}
			}
		}
	}
	for _, t := range targets {
		d, err := db.GetMediaUserData(user.ID, t.ItemID, t.EpisodeNo)
		if err != nil || d == nil {
			d = &model.MediaUserData{UserID: user.ID, ItemID: t.ItemID, EpisodeIndex: t.EpisodeNo}
		}
		d.Played = played
		d.PositionTicks = 0
		if !played {
			d.PlayCount = 0
		}
		_ = db.SaveMediaUserData(d)
	}
	c.Status(204)
}

func embySetFavorite(c *gin.Context, user *model.User, id string, fav bool) {
	lib := loadEmbyLibrary(user)
	target, ok := lib.playTarget(id)
	if !ok {
		embyNotFound(c)
		return
	}
	d, err := db.GetMediaUserData(user.ID, target.ItemID, target.EpisodeNo)
	if err != nil || d == nil {
		d = &model.MediaUserData{UserID: user.ID, ItemID: target.ItemID, EpisodeIndex: target.EpisodeNo}
	}
	d.IsFavorite = fav
	_ = db.SaveMediaUserData(d)
	c.JSON(200, gin.H{
		"PlaybackPositionTicks": d.PositionTicks,
		"PlayCount":             d.PlayCount,
		"Played":                d.Played,
		"IsFavorite":            d.IsFavorite,
	})
}

// embyUpdateUserData 部分更新用户状态
func embyUpdateUserData(c *gin.Context, user *model.User, id string) {
	lib := loadEmbyLibrary(user)
	target, ok := lib.playTarget(id)
	if !ok {
		embyNotFound(c)
		return
	}
	var body map[string]any
	_ = c.ShouldBindJSON(&body)
	d, err := db.GetMediaUserData(user.ID, target.ItemID, target.EpisodeNo)
	if err != nil || d == nil {
		d = &model.MediaUserData{UserID: user.ID, ItemID: target.ItemID, EpisodeIndex: target.EpisodeNo}
	}
	if v, ok := body["Played"].(bool); ok {
		d.Played = v
		if v {
			d.PositionTicks = 0
		}
	}
	if v, ok := body["IsFavorite"].(bool); ok {
		d.IsFavorite = v
	}
	if v, ok := body["PlaybackPositionTicks"].(float64); ok {
		d.PositionTicks = int64(v)
	}
	if v, ok := body["PlayCount"].(float64); ok {
		d.PlayCount = int(v)
	}
	_ = db.SaveMediaUserData(d)
	c.JSON(200, gin.H{
		"PlaybackPositionTicks": d.PositionTicks,
		"PlayCount":             d.PlayCount,
		"Played":                d.Played,
		"IsFavorite":            d.IsFavorite,
	})
}

// embyRefreshItem 触发条目所属扫描路径的增量扫描
func embyRefreshItem(c *gin.Context, id string) {
	lib := loadEmbyLibrary(nil)
	var itemID uint
	switch {
	case strings.HasPrefix(id, "mv-"), strings.HasPrefix(id, "so-"):
		n, _ := strconv.ParseUint(id[3:], 10, 64)
		itemID = uint(n)
	case strings.HasPrefix(id, "ep-"):
		parts := strings.Split(id, "-")
		if len(parts) == 3 {
			n, _ := strconv.ParseUint(parts[1], 10, 64)
			itemID = uint(n)
		}
	case strings.HasPrefix(id, "sr-"):
		if s := lib.seriesByKey(strings.TrimPrefix(id, "sr-")); s != nil && len(s.Items) > 0 {
			itemID = s.Items[0].ID
		}
	case strings.HasPrefix(id, "al-"):
		if al := lib.albumByKey(strings.TrimPrefix(id, "al-")); al != nil && len(al.Items) > 0 {
			itemID = al.Items[0].ID
		}
	}
	if itemID == 0 {
		c.Status(204)
		return
	}
	item, err := db.GetMediaItemByID(itemID)
	if err != nil {
		c.Status(204)
		return
	}
	if sp, err := db.GetMediaScanPath(item.ScanPathID); err == nil && sp != nil {
		media.ScanMediaPath(sp)
	}
	c.Status(204)
}

// ==================== 内部工具 ====================

func common_ApiBase(c *gin.Context) string {
	return strings.TrimSuffix(common.GetApiUrl(c.Request.Context()), "/")
}

// embyBodyJSON 便于调试时输出请求体
func embyBodyJSON(c *gin.Context) string {
	raw, _ := c.GetRawData()
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
