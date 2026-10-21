package handles

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	stdpath "path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/media"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
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
	streamURL := fmt.Sprintf("%s/@emby/Videos/%s/stream?Static=true", api, id)
	if target.Item.MediaType == model.MediaTypeMusic {
		streamURL = fmt.Sprintf("%s/@emby/Audio/%s/stream?Static=true", api, id)
	}
	source := embyMediaSource{
		Protocol:             "Http",
		Id:                   id,
		Path:                 target.RawPath,
		Type:                 "Default",
		Container:            target.Container,
		Name:                 target.Name,
		Size:                 target.Size,
		RunTimeTicks:         embyTicks(target.Duration),
		SupportsDirectPlay:   true,
		SupportsDirectStream: true,
		SupportsTranscoding:  false,
		DirectStreamUrl:      streamURL,
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
	user, _ := embyAuth(c)
	lib := loadEmbyLibrary(user)
	item, ok := lib.resolveItem(id)
	if !ok {
		embyNotFound(c)
		return
	}
	_ = item
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
		if ep := lib.findEpisode(id); ep != nil {
			mediaItem = ep.Item
		}
	case strings.HasPrefix(id, "sr-"):
		if s := lib.seriesByKey(strings.TrimPrefix(id, "sr-")); s != nil && len(s.Items) > 0 {
			mediaItem = &s.Items[0]
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
		mime := strings.TrimPrefix(cover[:idx], "data:")
		data, err := base64.StdEncoding.DecodeString(cover[idx+8:])
		if err != nil {
			c.Status(404)
			return
		}
		c.Header("Cache-Control", "public, max-age=86400")
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
