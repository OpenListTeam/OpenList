package handles

import (
	"fmt"
	"math/rand"
	"net/http"
	stdpath "path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/media"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/gin-gonic/gin"
)

// ==================== 浏览 ====================

func subInitial(name string) string {
	if name == "" {
		return "#"
	}
	r := strings.ToUpper(string([]rune(name)[0]))
	if r < "A" || r > "Z" {
		return "#"
	}
	return r
}

func subGetIndexes(c *gin.Context, user *model.User) {
	lib := loadEmbyLibrary(user)
	artists := lib.subArtists()
	groups := map[string][]subsonicChild{}
	var keys []string
	for _, a := range artists {
		idx := subInitial(a.Name)
		if _, ok := groups[idx]; !ok {
			keys = append(keys, idx)
		}
		groups[idx] = append(groups[idx], subsonicChild{
			Id:    a.Id,
			Title: a.Name,
			IsDir: true,
			Album: a.Name,
		})
	}
	sort.Strings(keys)
	out := &subsonicIndexes{IgnoredArticles: "The El La Los Las Le Les", LastModified: time.Now().UnixMilli()}
	for _, k := range keys {
		out.Index = append(out.Index, subsonicIndex{Name: k, Artist: groups[k]})
	}
	subRespond(c, &subsonicResponse{Indexes: out})
}

func subGetArtists(c *gin.Context, user *model.User) {
	lib := loadEmbyLibrary(user)
	artists := lib.subArtists()
	groups := map[string][]subsonicArtist{}
	var keys []string
	for _, a := range artists {
		idx := subInitial(a.Name)
		if _, ok := groups[idx]; !ok {
			keys = append(keys, idx)
		}
		groups[idx] = append(groups[idx], a)
	}
	sort.Strings(keys)
	out := &subsonicArtists{IgnoredArticles: "The El La Los Las Le Les"}
	for _, k := range keys {
		out.Index = append(out.Index, subsonicIndex2{Name: k, Artist: groups[k]})
	}
	subRespond(c, &subsonicResponse{Artists: out})
}

func subGetArtist(c *gin.Context, user *model.User) {
	id := subParam(c, "id")
	if !strings.HasPrefix(id, "ar-") {
		subError(c, 70, "artist not found")
		return
	}
	lib := loadEmbyLibrary(user)
	key := strings.TrimPrefix(id, "ar-")
	for _, a := range lib.subArtists() {
		if a.Id == id {
			_ = key
			subRespond(c, &subsonicResponse{Artist: &a})
			return
		}
	}
	subError(c, 70, "artist not found")
}

func subGetAlbum(c *gin.Context, user *model.User) {
	id := subParam(c, "id")
	if !strings.HasPrefix(id, "al-") {
		subError(c, 70, "album not found")
		return
	}
	lib := loadEmbyLibrary(user)
	al := lib.albumByKey(strings.TrimPrefix(id, "al-"))
	if al == nil {
		subError(c, 70, "album not found")
		return
	}
	out := lib.subAlbumWithSongs(al)
	subRespond(c, &subsonicResponse{Album: &out})
}

func subGetSong(c *gin.Context, user *model.User) {
	id := subParam(c, "id")
	lib := loadEmbyLibrary(user)
	it, ok := lib.songByID(id)
	if !ok {
		subError(c, 70, "song not found")
		return
	}
	child := lib.subChild(it)
	subRespond(c, &subsonicResponse{Song: &child})
}

func subGetMusicDirectory(c *gin.Context, user *model.User) {
	id := subParam(c, "id")
	lib := loadEmbyLibrary(user)
	switch {
	case strings.HasPrefix(id, "ar-"):
		key := strings.TrimPrefix(id, "ar-")
		dir := &subsonicDirectory{Id: id, Name: id}
		for _, al := range lib.albums() {
			if artistKeyOf(&al.Items[0]) != key {
				continue
			}
			a := lib.subAlbum(al)
			dir.Child = append(dir.Child, subsonicChild{
				Id: a.Id, IsDir: true, Title: a.Name, Album: a.Name,
				Artist: a.Artist, CoverArt: a.CoverArt, Year: a.Year,
			})
			if dir.Name == id {
				dir.Name = a.Artist
			}
		}
		subRespond(c, &subsonicResponse{MusicDirectory: dir})
	case strings.HasPrefix(id, "al-"):
		al := lib.albumByKey(strings.TrimPrefix(id, "al-"))
		if al == nil {
			subError(c, 70, "directory not found")
			return
		}
		dir := &subsonicDirectory{Id: id, Name: al.Name, Parent: "ar-" + artistKeyOf(&al.Items[0])}
		for i := range al.Items {
			dir.Child = append(dir.Child, lib.subChild(&al.Items[i]))
		}
		subRespond(c, &subsonicResponse{MusicDirectory: dir})
	default:
		subError(c, 70, "directory not found")
	}
}

func subGetAlbumList(c *gin.Context, user *model.User, legacy bool) {
	lib := loadEmbyLibrary(user)
	listType := strings.ToLower(subParam(c, "type"))
	albums := lib.albums()
	switch listType {
	case "newest":
		sort.SliceStable(albums, func(i, j int) bool { return albums[i].Items[0].CreatedAt.After(albums[j].Items[0].CreatedAt) })
	case "recent":
		sort.SliceStable(albums, func(i, j int) bool { return albums[i].Items[0].UpdatedAt.After(albums[j].Items[0].UpdatedAt) })
	case "alphabeticalbyartist":
		sort.SliceStable(albums, func(i, j int) bool {
			if albums[i].Artist == albums[j].Artist {
				return strings.ToLower(albums[i].Name) < strings.ToLower(albums[j].Name)
			}
			return strings.ToLower(albums[i].Artist) < strings.ToLower(albums[j].Artist)
		})
	case "random":
		rand.Shuffle(len(albums), func(i, j int) { albums[i], albums[j] = albums[j], albums[i] })
	case "byyear":
		from := subIntParam(c, "fromYear", 0)
		to := subIntParam(c, "toYear", 0)
		var filtered []*embyAlbum
		for _, al := range albums {
			if from > 0 && al.Year < from {
				continue
			}
			if to > 0 && al.Year > to {
				continue
			}
			filtered = append(filtered, al)
		}
		albums = filtered
	default:
		sort.SliceStable(albums, func(i, j int) bool { return strings.ToLower(albums[i].Name) < strings.ToLower(albums[j].Name) })
	}
	size := subIntParam(c, "size", 50)
	offset := subIntParam(c, "offset", 0)
	if offset > len(albums) {
		offset = len(albums)
	}
	end := offset + size
	if end > len(albums) {
		end = len(albums)
	}
	out := &subsonicAlbumList{}
	for _, al := range albums[offset:end] {
		out.Album = append(out.Album, lib.subAlbum(al))
	}
	if legacy {
		subRespond(c, &subsonicResponse{AlbumList: out})
		return
	}
	subRespond(c, &subsonicResponse{AlbumList2: out})
}

func (l *embyLibrary) allSongs() []subsonicChild {
	out := make([]subsonicChild, 0, len(l.music))
	for i := range l.music {
		out = append(out, l.subChild(&l.music[i]))
	}
	return out
}

func subGetRandomSongs(c *gin.Context, user *model.User) {
	lib := loadEmbyLibrary(user)
	songs := lib.allSongs()
	rand.Shuffle(len(songs), func(i, j int) { songs[i], songs[j] = songs[j], songs[i] })
	size := subIntParam(c, "size", 10)
	if size > len(songs) {
		size = len(songs)
	}
	subRespond(c, &subsonicResponse{RandomSongs: &subsonicSongs{Song: songs[:size]}})
}

func subGetSongsByGenre(c *gin.Context, user *model.User) {
	lib := loadEmbyLibrary(user)
	genre := strings.ToLower(subParam(c, "genre"))
	var songs []subsonicChild
	for i := range lib.music {
		if genre != "" && !strings.Contains(strings.ToLower(lib.music[i].Genre), genre) {
			continue
		}
		songs = append(songs, lib.subChild(&lib.music[i]))
	}
	subRespond(c, &subsonicResponse{SongsByGenre: &subsonicSongs{Song: songs}})
}

func subSearch(c *gin.Context, user *model.User, legacy bool) {
	lib := loadEmbyLibrary(user)
	term := strings.ToLower(strings.TrimSpace(subParam(c, "query")))
	if term == "" {
		term = strings.ToLower(strings.TrimSpace(subParam(c, "any")))
	}
	artistCount := subIntParam(c, "artistCount", 20)
	albumCount := subIntParam(c, "albumCount", 20)
	songCount := subIntParam(c, "songCount", 20)

	out := &subsonicSearchResult{}
	for _, a := range lib.subArtists() {
		if len(out.Artist) >= artistCount {
			break
		}
		if term == "" || strings.Contains(strings.ToLower(a.Name), term) {
			out.Artist = append(out.Artist, a)
		}
	}
	for _, al := range lib.albums() {
		if len(out.Album) >= albumCount {
			break
		}
		if term == "" || strings.Contains(strings.ToLower(al.Name), term) ||
			strings.Contains(strings.ToLower(al.Artist), term) {
			out.Album = append(out.Album, lib.subAlbum(al))
		}
	}
	for i := range lib.music {
		if len(out.Song) >= songCount {
			break
		}
		name := lib.music[i].ScrapedName
		if name == "" {
			name = lib.music[i].FileName
		}
		if term == "" || strings.Contains(strings.ToLower(name), term) {
			out.Song = append(out.Song, lib.subChild(&lib.music[i]))
		}
	}
	out.Total = len(out.Artist) + len(out.Album) + len(out.Song)
	if legacy {
		subRespond(c, &subsonicResponse{SearchResult2: out})
		return
	}
	subRespond(c, &subsonicResponse{SearchResult3: out})
}

func subGetStarred(c *gin.Context, user *model.User, legacy bool) {
	lib := loadEmbyLibrary(user)
	out := &subsonicStarred{}
	for i := range lib.music {
		if d, ok := lib.userData[[2]uint{lib.music[i].ID, 0}]; ok && d.IsFavorite {
			out.Song = append(out.Song, lib.subChild(&lib.music[i]))
		}
	}
	if legacy {
		subRespond(c, &subsonicResponse{Starred: out})
		return
	}
	subRespond(c, &subsonicResponse{Starred2: out})
}

func subGetGenres(c *gin.Context, user *model.User) {
	lib := loadEmbyLibrary(user)
	counts := map[string]int{}
	for i := range lib.music {
		for _, g := range strings.Split(lib.music[i].Genre, ",") {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			counts[g]++
		}
	}
	out := &subsonicGenres{}
	for g, n := range counts {
		out.Genre = append(out.Genre, subsonicGenre{Value: g, SongCount: n})
	}
	sort.Slice(out.Genre, func(i, j int) bool { return out.Genre[i].Value < out.Genre[j].Value })
	subRespond(c, &subsonicResponse{Genres: out})
}

// ==================== 播放 ====================

func subStream(c *gin.Context, user *model.User) {
	id := subParam(c, "id")
	lib := loadEmbyLibrary(user)
	target, ok := lib.playTarget(id)
	if !ok {
		subError(c, 70, "song not found")
		return
	}
	if setting.GetBool(conf.MediaSubsonicProxyStream) {
		c.Redirect(http.StatusFound, SignedProxyURL(c, target.RawPath))
		return
	}
	c.Redirect(http.StatusFound, SignedStreamURL(c, target.RawPath))
}

func subGetCoverArt(c *gin.Context, user *model.User) {
	id := subParam(c, "id")
	lib := loadEmbyLibrary(user)
	var item *model.MediaItem
	switch {
	case strings.HasPrefix(id, "so-"):
		item, _ = lib.songByID(id)
	case strings.HasPrefix(id, "al-"):
		if al := lib.albumByKey(strings.TrimPrefix(id, "al-")); al != nil && len(al.Items) > 0 {
			item = &al.Items[0]
		}
	case strings.HasPrefix(id, "ar-"):
		key := strings.TrimPrefix(id, "ar-")
		for i := range lib.music {
			if artistKeyOf(&lib.music[i]) == key {
				item = &lib.music[i]
				break
			}
		}
	case strings.HasPrefix(id, "mv-"), strings.HasPrefix(id, "ep-"):
		if it, ok := lib.resolveItem(id); ok {
			_ = it
			if p, ok := lib.playTarget(id); ok {
				item = p.Item
			}
		}
	}
	if item == nil {
		c.Status(404)
		return
	}
	serveEmbyCover(c, item)
}

func subGetLyrics(c *gin.Context, user *model.User) {
	artist := subParam(c, "artist")
	title := subParam(c, "title")
	lib := loadEmbyLibrary(user)
	for i := range lib.music {
		it := &lib.music[i]
		name := it.ScrapedName
		if name == "" {
			name = strings.TrimSuffix(it.FileName, stdpath.Ext(it.FileName))
		}
		if title != "" && !strings.EqualFold(title, name) {
			continue
		}
		if artist != "" && !strings.EqualFold(artist, albumArtistOf(it)) {
			continue
		}
		subRespond(c, &subsonicResponse{Lyrics: &subsonicLyrics{
			Artist: albumArtistOf(it), Title: name, Value: it.Lyrics,
		}})
		return
	}
	subError(c, 70, "lyrics not found")
}

func subGetLyricsBySongID(c *gin.Context, user *model.User) {
	id := subParam(c, "id")
	lib := loadEmbyLibrary(user)
	it, ok := lib.songByID(id)
	if !ok || it.Lyrics == "" {
		subError(c, 70, "lyrics not found")
		return
	}
	name := it.ScrapedName
	if name == "" {
		name = strings.TrimSuffix(it.FileName, stdpath.Ext(it.FileName))
	}
	lines := parseLrc(it.Lyrics)
	structured := subsonicStructuredLyrics{
		DisplayArtist: albumArtistOf(it),
		DisplayTitle:  name,
		Lang:          "und",
		Synced:        len(lines) > 0,
	}
	for _, l := range lines {
		structured.Line = append(structured.Line, subsonicLyricLine{
			Start: l["Start"].(int64),
			Value: l["Text"].(string),
		})
	}
	subRespond(c, &subsonicResponse{LyricsList: &subsonicLyricsList{
		StructuredLyrics: []subsonicStructuredLyrics{structured},
	}})
}

// ==================== 状态 ====================

func subStar(c *gin.Context, user *model.User, star bool) {
	id := subParam(c, "id")
	lib := loadEmbyLibrary(user)
	target, ok := lib.playTarget(id)
	if !ok {
		if album := strings.HasPrefix(id, "al-"); album {
			subError(c, 70, "not supported")
			return
		}
		subError(c, 70, "song not found")
		return
	}
	d, err := db.GetMediaUserData(user.ID, target.ItemID, target.EpisodeNo)
	if err != nil || d == nil {
		d = &model.MediaUserData{UserID: user.ID, ItemID: target.ItemID, EpisodeIndex: target.EpisodeNo}
	}
	d.IsFavorite = star
	if err := db.SaveMediaUserData(d); err != nil {
		subError(c, 0, err.Error())
		return
	}
	subRespond(c, &subsonicResponse{})
}

func subSetRating(c *gin.Context, user *model.User) {
	id := subParam(c, "id")
	rating := subIntParam(c, "rating", 0)
	lib := loadEmbyLibrary(user)
	target, ok := lib.playTarget(id)
	if !ok {
		subError(c, 70, "song not found")
		return
	}
	d, err := db.GetMediaUserData(user.ID, target.ItemID, target.EpisodeNo)
	if err != nil || d == nil {
		d = &model.MediaUserData{UserID: user.ID, ItemID: target.ItemID, EpisodeIndex: target.EpisodeNo}
	}
	d.Rating = rating
	_ = db.SaveMediaUserData(d)
	subRespond(c, &subsonicResponse{})
}

func subScrobble(c *gin.Context, user *model.User) {
	id := subParam(c, "id")
	submission := strings.EqualFold(subParam(c, "submission"), "true")
	lib := loadEmbyLibrary(user)
	target, ok := lib.playTarget(id)
	if !ok {
		subRespond(c, &subsonicResponse{})
		return
	}
	d, err := db.GetMediaUserData(user.ID, target.ItemID, target.EpisodeNo)
	if err != nil || d == nil {
		d = &model.MediaUserData{UserID: user.ID, ItemID: target.ItemID, EpisodeIndex: target.EpisodeNo}
	}
	now := time.Now()
	d.LastPlayedAt = &now
	if submission {
		d.Played = true
		d.PlayCount++
	}
	_ = db.SaveMediaUserData(d)
	subRespond(c, &subsonicResponse{})
}

// ==================== 播放列表 ====================

func subGetPlaylists(c *gin.Context, user *model.User) {
	list, err := db.ListMediaPlaylists(user.ID)
	if err != nil {
		subError(c, 0, err.Error())
		return
	}
	out := &subsonicPlaylists{}
	for i := range list {
		out.Playlist = append(out.Playlist, subPlaylistOf(&list[i]))
	}
	subRespond(c, &subsonicResponse{Playlists: out})
}

func subPlaylistOf(p *model.MediaPlaylist) subsonicPlaylist {
	return subsonicPlaylist{
		Id:        strconv.FormatUint(uint64(p.ID), 10),
		Name:      p.Name,
		Comment:   p.Comment,
		Public:    p.Public,
		SongCount: p.ItemCount,
		Created:   p.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

func subGetPlaylist(c *gin.Context, user *model.User) {
	id, err := strconv.ParseUint(subParam(c, "id"), 10, 64)
	if err != nil {
		subError(c, 70, "playlist not found")
		return
	}
	p, err := db.GetMediaPlaylist(uint(id))
	if err != nil || (p.UserID != user.ID && !user.IsAdmin()) {
		subError(c, 70, "playlist not found")
		return
	}
	out := subPlaylistOf(p)
	items, _ := db.ListMediaPlaylistItems(p.ID)
	lib := loadEmbyLibrary(user)
	for _, it := range items {
		if song, ok := lib.songByID(fmt.Sprintf("so-%d", it.ItemID)); ok {
			out.Entry = append(out.Entry, lib.subChild(song))
		}
	}
	out.SongCount = len(out.Entry)
	subRespond(c, &subsonicResponse{Playlist: &out})
}

func subCreatePlaylist(c *gin.Context, user *model.User) {
	name := subParam(c, "name")
	if name == "" {
		subError(c, 10, "missing name")
		return
	}
	p := &model.MediaPlaylist{UserID: user.ID, Name: name}
	if err := db.CreateMediaPlaylist(p); err != nil {
		subError(c, 0, err.Error())
		return
	}
	subError(c, 0, "created:"+strconv.FormatUint(uint64(p.ID), 10))
}

func subUpdatePlaylist(c *gin.Context, user *model.User) {
	id, err := strconv.ParseUint(subParam(c, "playlistId"), 10, 64)
	if err != nil {
		subError(c, 10, "missing playlistId")
		return
	}
	p, err := db.GetMediaPlaylist(uint(id))
	if err != nil || (p.UserID != user.ID && !user.IsAdmin()) {
		subError(c, 70, "playlist not found")
		return
	}
	if name := subParam(c, "name"); name != "" {
		p.Name = name
	}
	if comment := subParam(c, "comment"); comment != "" {
		p.Comment = comment
	}
	if pub := subParam(c, "public"); pub != "" {
		p.Public = strings.EqualFold(pub, "true")
	}
	_ = db.UpdateMediaPlaylist(p)
	subRespond(c, &subsonicResponse{})
}

func subDeletePlaylist(c *gin.Context, user *model.User) {
	id, err := strconv.ParseUint(subParam(c, "id"), 10, 64)
	if err != nil {
		subError(c, 10, "missing id")
		return
	}
	p, err := db.GetMediaPlaylist(uint(id))
	if err != nil || (p.UserID != user.ID && !user.IsAdmin()) {
		subError(c, 70, "playlist not found")
		return
	}
	_ = db.DeleteMediaPlaylist(p.ID)
	subRespond(c, &subsonicResponse{})
}

// ==================== 扫描 ====================

func subSongCount(user *model.User) int64 {
	lib := loadEmbyLibrary(user)
	return int64(len(lib.music))
}

func subStartScan(c *gin.Context) {
	for _, mt := range MediaTypeEnabledList() {
		cfg, err := db.GetMediaConfig(mt)
		if err != nil || cfg == nil {
			continue
		}
		media.ScanMedia(cfg)
	}
	subRespond(c, &subsonicResponse{ScanStatus: &subsonicScanStatus{Scanning: true}})
}

// ==================== 播放队列（内存） ====================

var (
	subQueueMu sync.Mutex
	subQueues  = map[uint]*subsonicPlayQueue{}
)

func subSavePlayQueue(c *gin.Context, user *model.User) {
	ids := c.Query("id")
	if ids == "" {
		_ = c.Request.ParseForm()
		ids = c.Request.PostForm.Get("id")
	}
	q := &subsonicPlayQueue{
		Current:  subParam(c, "current"),
		Position: int64(subIntParam(c, "position", 0)),
		Username: user.Username,
		Changed:  time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if ids != "" {
		lib := loadEmbyLibrary(user)
		for _, id := range strings.Split(ids, ",") {
			if song, ok := lib.songByID(strings.TrimSpace(id)); ok {
				q.Entry = append(q.Entry, lib.subChild(song))
			}
		}
	}
	subQueueMu.Lock()
	subQueues[user.ID] = q
	subQueueMu.Unlock()
	subRespond(c, &subsonicResponse{})
}

func subLoadPlayQueue(user *model.User) *subsonicPlayQueue {
	subQueueMu.Lock()
	defer subQueueMu.Unlock()
	if q, ok := subQueues[user.ID]; ok {
		return q
	}
	return &subsonicPlayQueue{Username: user.Username}
}

func subNowPlayingEntries() []subsonicChild { return []subsonicChild{} }
