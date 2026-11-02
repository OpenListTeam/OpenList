package handles

import (
	"encoding/json"
	"fmt"
	stdpath "path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/gin-gonic/gin"
)

// ==================== 媒体库索引 ====================

type embyLibrary struct {
	user   *model.User
	videos []model.MediaItem
	music  []model.MediaItem

	userData map[[2]uint]*model.MediaUserData
	// ids 保存 GUID -> 内部可读 ID 的映射，客户端只会回传 GUID
	ids map[string]string
}

func loadEmbyLibrary(user *model.User) *embyLibrary {
	l := &embyLibrary{
		user:     user,
		userData: map[[2]uint]*model.MediaUserData{},
		ids:      map[string]string{},
	}
	if MediaTypeEnabled(model.MediaTypeVideo) {
		items, err := db.ListAllMediaItems(model.MediaTypeVideo)
		if err == nil {
			for i := range items {
				if CanUserAccessItem(user, &items[i]) {
					l.videos = append(l.videos, items[i])
				}
			}
		}
	}
	if MediaTypeEnabled(model.MediaTypeMusic) {
		items, err := db.ListAllMediaItems(model.MediaTypeMusic)
		if err == nil {
			for i := range items {
				if CanUserAccessItem(user, &items[i]) {
					l.music = append(l.music, items[i])
				}
			}
		}
	}
	ids := make([]uint, 0, len(l.videos)+len(l.music))
	for _, it := range l.videos {
		ids = append(ids, it.ID)
	}
	for _, it := range l.music {
		ids = append(ids, it.ID)
	}
	if user != nil && len(ids) > 0 {
		if list, err := db.ListMediaUserDataByItems(user.ID, ids); err == nil {
			for i := range list {
				d := list[i]
				l.userData[[2]uint{d.ItemID, uint(d.EpisodeIndex)}] = &d
			}
		}
	}
	l.registerIDs()
	return l
}

// guid 返回内部 ID 对应的对外数字 ID，并登记反查映射。
// 名称保留 guid 是为了少改调用点，实际输出的是纯数字（Emby Item Id 规范）。
func (l *embyLibrary) guid(orig string) string {
	g := embyNumericID(orig)
	if l.ids != nil {
		l.ids[g] = orig
		// 兼容旧版本缓存下来的 GUID 形式 ID：客户端没清缓存时仍能解析，
		// 否则会一直拿旧 ID 请求并得到 {"Error":"Not Found"}
		l.ids[embyGUID(orig)] = orig
	}
	return g
}

// origID 把客户端回传的数字 ID 还原为内部 ID；已是内部 ID 时原样返回
func (l *embyLibrary) origID(id string) string {
	id = strings.TrimSpace(id)
	if l.ids != nil {
		if orig, ok := l.ids[id]; ok {
			return orig
		}
	}
	return id
}

// registerIDs 预先登记全部条目的对外 ID，保证任意请求都能反查出内部 ID
func (l *embyLibrary) registerIDs() {
	register := func(orig string) {
		l.ids[embyNumericID(orig)] = orig
		l.ids[embyGUID(orig)] = orig
	}
	register("root")
	register("lib-movie")
	register("lib-tv")
	register("lib-music")
	for i := range l.videos {
		register(fmt.Sprintf("mv-%d", l.videos[i].ID))
	}
	for _, s := range l.seriesList() {
		register("sr-" + s.Key)
		for _, season := range l.seasonList(s) {
			register(fmt.Sprintf("sn-%s-%d", s.Key, season))
		}
		for _, ep := range l.episodesOf(s.Key, 0) {
			register(ep.ID)
		}
	}
	for i := range l.music {
		it := &l.music[i]
		register(fmt.Sprintf("so-%d", it.ID))
		register("al-" + albumKeyOf(it))
		register("ar-" + artistKeyOf(it))
	}
}

func (l *embyLibrary) userDataOf(itemID uint, episodeIndex int) *embyUserData {
	if l == nil || l.user == nil {
		return &embyUserData{}
	}
	d, ok := l.userData[[2]uint{itemID, uint(episodeIndex)}]
	if !ok {
		return &embyUserData{}
	}
	return &embyUserData{
		PlaybackPositionTicks: d.PositionTicks,
		PlayCount:             d.PlayCount,
		IsFavorite:            d.IsFavorite,
		Played:                d.Played,
	}
}

type embySeries struct {
	Key      string
	Name     string
	Items    []model.MediaItem
	Year     int
	Overview string
	Cover    string
}

// isEmbySeriesItem 判断视频条目是否属于剧集。
// 电视剧按目录扫描（IsFolder=true，带 Episodes 选集），或被刮削为 tv；
// 其余单文件视频一律按电影处理。仅看 video_type 会把目录型剧集误判为电影。
func isEmbySeriesItem(it *model.MediaItem) bool {
	return it.IsFolder || it.VideoType == "tv"
}

func seriesKeyOf(item *model.MediaItem) string {
	name := item.ScrapedName
	if name == "" {
		name = strings.TrimSuffix(stdpath.Base(mediaFolderOf(item)), "/")
	}
	return embyHash("series:" + strings.ToLower(name))
}

func seriesNameOf(item *model.MediaItem) string {
	if item.ScrapedName != "" {
		return item.ScrapedName
	}
	if item.IsFolder {
		return strings.TrimSuffix(item.FileName, stdpath.Ext(item.FileName))
	}
	return strings.TrimSuffix(stdpath.Base(mediaFolderOf(item)), "/")
}

// seriesList 汇总剧集（按剧名去重）
func (l *embyLibrary) seriesList() []*embySeries {
	index := map[string]*embySeries{}
	var order []string
	for i := range l.videos {
		it := &l.videos[i]
		if !isEmbySeriesItem(it) {
			continue
		}
		key := seriesKeyOf(it)
		s, ok := index[key]
		if !ok {
			s = &embySeries{Key: key, Name: seriesNameOf(it), Cover: it.Cover}
			if len(it.ReleaseDate) >= 4 {
				s.Year, _ = strconv.Atoi(it.ReleaseDate[:4])
			}
			s.Overview = it.Plot
			index[key] = s
			order = append(order, key)
		}
		if s.Cover == "" {
			s.Cover = it.Cover
		}
		if s.Overview == "" {
			s.Overview = it.Plot
		}
		s.Items = append(s.Items, *it)
	}
	out := make([]*embySeries, 0, len(order))
	for _, k := range order {
		out = append(out, index[k])
	}
	return out
}

func (l *embyLibrary) seriesByKey(key string) *embySeries {
	for _, s := range l.seriesList() {
		if s.Key == key {
			return s
		}
	}
	return nil
}

type embyEpisode struct {
	ID         string
	Item       *model.MediaItem
	Episode    model.EpisodeInfo
	Index      int
	Season     int
	SeriesKey  string
	SeriesName string
}

func parseEpisodeList(item *model.MediaItem) []model.EpisodeInfo {
	if item.Episodes == "" {
		return nil
	}
	var eps []model.EpisodeInfo
	if err := json.Unmarshal([]byte(item.Episodes), &eps); err != nil {
		return nil
	}
	return eps
}

// episodesOf 返回某部剧（可按季过滤）的集列表
func (l *embyLibrary) episodesOf(seriesKey string, season int) []*embyEpisode {
	var out []*embyEpisode
	for i := range l.videos {
		it := &l.videos[i]
		if !isEmbySeriesItem(it) || seriesKeyOf(it) != seriesKey {
			continue
		}
		s := it.Season
		if s <= 0 {
			s = 1
		}
		if season > 0 && s != season {
			continue
		}
		if it.IsFolder {
			for idx, ep := range parseEpisodeList(it) {
				num := ep.Index
				if num <= 0 {
					num = idx + 1
				}
				out = append(out, &embyEpisode{
					ID:         fmt.Sprintf("ep-%d-%d", it.ID, num),
					Item:       it,
					Episode:    ep,
					Index:      num,
					Season:     s,
					SeriesKey:  seriesKey,
					SeriesName: seriesNameOf(it),
				})
			}
			continue
		}
		num := it.Episode
		if num <= 0 {
			num = 1
		}
		out = append(out, &embyEpisode{
			ID:         fmt.Sprintf("ep-%d-%d", it.ID, num),
			Item:       it,
			Index:      num,
			Season:     s,
			SeriesKey:  seriesKey,
			SeriesName: seriesNameOf(it),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Season != out[j].Season {
			return out[i].Season < out[j].Season
		}
		return out[i].Index < out[j].Index
	})
	return out
}

func (l *embyLibrary) findEpisode(id string) *embyEpisode {
	// id: ep-<itemID>-<num>
	parts := strings.Split(id, "-")
	if len(parts) != 3 {
		return nil
	}
	itemID, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return nil
	}
	num, _ := strconv.Atoi(parts[2])
	item, err := db.GetMediaItemByID(uint(itemID))
	if err != nil || !CanUserAccessItem(l.user, item) {
		return nil
	}
	season := item.Season
	if season <= 0 {
		season = 1
	}
	if item.IsFolder {
		for i, ep := range parseEpisodeList(item) {
			n := ep.Index
			if n <= 0 {
				n = i + 1
			}
			if n == num {
				return &embyEpisode{ID: id, Item: item, Episode: ep, Index: n, Season: season,
					SeriesKey: seriesKeyOf(item), SeriesName: seriesNameOf(item)}
			}
		}
		return nil
	}
	return &embyEpisode{ID: id, Item: item, Index: num, Season: season,
		SeriesKey: seriesKeyOf(item), SeriesName: seriesNameOf(item)}
}

// music 索引

type embyAlbum struct {
	Key    string
	Name   string
	Artist string
	Cover  string
	Year   int
	Items  []model.MediaItem
}

func albumArtistOf(item *model.MediaItem) string {
	if item.AlbumArtist != "" {
		return item.AlbumArtist
	}
	if item.Authors != "" {
		return item.Authors
	}
	return "Unknown Artist"
}

func albumKeyOf(item *model.MediaItem) string {
	album := item.AlbumName
	if album == "" {
		album = "Unknown Album"
	}
	return embyHash("album:" + strings.ToLower(albumArtistOf(item)) + "\x00" + strings.ToLower(album))
}

func artistKeyOf(item *model.MediaItem) string {
	return embyHash("artist:" + strings.ToLower(albumArtistOf(item)))
}

func (l *embyLibrary) albums() []*embyAlbum {
	index := map[string]*embyAlbum{}
	var order []string
	for i := range l.music {
		it := &l.music[i]
		key := albumKeyOf(it)
		al, ok := index[key]
		if !ok {
			name := it.AlbumName
			if name == "" {
				name = "Unknown Album"
			}
			al = &embyAlbum{Key: key, Name: name, Artist: albumArtistOf(it), Cover: it.Cover}
			if len(it.ReleaseDate) >= 4 {
				al.Year, _ = strconv.Atoi(it.ReleaseDate[:4])
			}
			index[key] = al
			order = append(order, key)
		}
		if al.Cover == "" {
			al.Cover = it.Cover
		}
		al.Items = append(al.Items, *it)
	}
	out := make([]*embyAlbum, 0, len(order))
	for _, k := range order {
		out = append(out, index[k])
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (l *embyLibrary) albumByKey(key string) *embyAlbum {
	for _, a := range l.albums() {
		if a.Key == key {
			return a
		}
	}
	return nil
}

// ==================== DTO 构造 ====================

func (l *embyLibrary) movieItem(it *model.MediaItem) *embyItem {
	year := 0
	if len(it.ReleaseDate) >= 4 {
		year, _ = strconv.Atoi(it.ReleaseDate[:4])
	}
	name := it.ScrapedName
	if name == "" {
		name = strings.TrimSuffix(it.FileName, stdpath.Ext(it.FileName))
	}
	e := &embyItem{
		Name:            name,
		SortName:        name,
		ServerId:        embyServerID(),
		Id:              l.guid(fmt.Sprintf("mv-%d", it.ID)),
		ParentId:        l.guid("lib-movie"),
		Type:            "Movie",
		MediaType:       "Video",
		IsFolder:        false,
		Path:            MediaItemFilePath(it),
		Container:       embyExt(it.FileName),
		RunTimeTicks:    embyTicks(it.Duration),
		ProductionYear:  year,
		Overview:        it.Plot,
		CommunityRating: it.Rating,
		PremiereDate:    it.ReleaseDate,
		DateCreated:     embyTimeStr(it.CreatedAt),
		UserData:        l.userDataOf(it.ID, 0),
		ImageTags:       map[string]string{},
		ProviderIds:     map[string]string{},
		LocationType:    "FileSystem",
	}
	if tag := embyImageTag(it); tag != "" {
		e.ImageTags["Primary"] = tag
	}
	if it.ExternalID != "" {
		e.ProviderIds["Tmdb"] = it.ExternalID
	}
	return e
}

func (l *embyLibrary) seriesItem(s *embySeries) *embyItem {
	e := &embyItem{
		Name:         s.Name,
		SortName:     s.Name,
		ServerId:     embyServerID(),
		Id:           l.guid("sr-" + s.Key),
		ParentId:     l.guid("lib-tv"),
		Type:         "Series",
		IsFolder:     true,
		ProductionYear: s.Year,
		Overview:     s.Overview,
		DateCreated:  embyTimeStr(s.Items[0].CreatedAt),
		ChildCount:   l.seasonCount(s),
		LocationType: "FileSystem",
		ImageTags:    map[string]string{},
		ProviderIds:  map[string]string{},
		UserData:     &embyUserData{},
	}
	if tag := embyImageTag(&s.Items[0]); tag != "" {
		e.ImageTags["Primary"] = tag
	}
	if s.Items[0].ExternalID != "" {
		e.ProviderIds["Tmdb"] = s.Items[0].ExternalID
	}
	return e
}

func (l *embyLibrary) seasonCount(s *embySeries) int {
	seasons := map[int]bool{}
	for i := range s.Items {
		season := s.Items[i].Season
		if season <= 0 {
			season = 1
		}
		seasons[season] = true
	}
	return len(seasons)
}

func (l *embyLibrary) seasonList(s *embySeries) []int {
	set := map[int]bool{}
	for i := range s.Items {
		season := s.Items[i].Season
		if season <= 0 {
			season = 1
		}
		set[season] = true
	}
	out := make([]int, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

func (l *embyLibrary) seasonItem(s *embySeries, season int) *embyItem {
	eps := l.episodesOf(s.Key, season)
	unplayed := 0
	for _, ep := range eps {
		if !l.userDataOf(ep.Item.ID, ep.Index).Played {
			unplayed++
		}
	}
	e := &embyItem{
		Name:         fmt.Sprintf("Season %d", season),
		SortName:     fmt.Sprintf("Season %02d", season),
		ServerId:     embyServerID(),
		Id:           l.guid(fmt.Sprintf("sn-%s-%d", s.Key, season)),
		Type:         "Season",
		IsFolder:     true,
		ParentId:     l.guid("sr-" + s.Key),
		SeriesId:     l.guid("sr-" + s.Key),
		SeriesName:   s.Name,
		IndexNumber:  season,
		ChildCount:   len(eps),
		LocationType: "FileSystem",
		ImageTags:    map[string]string{},
		UserData:     &embyUserData{UnplayedItemCount: unplayed},
	}
	if len(s.Items) > 0 {
		if tag := embyImageTag(&s.Items[0]); tag != "" {
			e.ImageTags["Primary"] = tag
		}
	}
	return e
}

func (l *embyLibrary) episodeItem(ep *embyEpisode) *embyItem {
	title := ep.Episode.Title
	if title == "" {
		title = fmt.Sprintf("Episode %d", ep.Index)
	}
	seriesID := "sr-" + ep.SeriesKey
	seasonID := fmt.Sprintf("sn-%s-%d", ep.SeriesKey, ep.Season)
	rawPath := MediaItemFilePath(ep.Item)
	if ep.Episode.FileName != "" {
		rawPath = stdpath.Join(mediaFolderOf(ep.Item), ep.Item.FileName, ep.Episode.FileName)
	}
	e := &embyItem{
		Name:              title,
		SortName:          fmt.Sprintf("%02d-%03d", ep.Season, ep.Index),
		ServerId:          embyServerID(),
		Id:                l.guid(ep.ID),
		Type:              "Episode",
		MediaType:         "Video",
		IsFolder:          false,
		ParentId:          l.guid(seasonID),
		SeriesId:          l.guid(seriesID),
		SeriesName:        ep.SeriesName,
		SeasonId:          l.guid(seasonID),
		SeasonName:        fmt.Sprintf("Season %d", ep.Season),
		IndexNumber:       ep.Index,
		ParentIndexNumber: ep.Season,
		Path:              rawPath,
		Container:         embyExt(rawPath),
		RunTimeTicks:      embyTicks(ep.Item.Duration),
		Overview:          ep.Item.Plot,
		DateCreated:       embyTimeStr(ep.Item.CreatedAt),
		UserData:          l.userDataOf(ep.Item.ID, ep.Index),
		ImageTags:         map[string]string{},
		ProviderIds:       map[string]string{},
		LocationType:      "FileSystem",
	}
	if tag := embyImageTag(ep.Item); tag != "" {
		e.ImageTags["Primary"] = tag
	}
	return e
}

func (l *embyLibrary) albumItem(al *embyAlbum) *embyItem {
	e := &embyItem{
		Name:           al.Name,
		SortName:       al.Name,
		ServerId:       embyServerID(),
		Id:             l.guid("al-" + al.Key),
		ParentId:       l.guid("lib-music"),
		Type:           "MusicAlbum",
		IsFolder:       true,
		AlbumArtist:    al.Artist,
		AlbumArtists:   []embyNameID{{Name: al.Artist, Id: l.guid("ar-" + artistKeyOf(&al.Items[0]))}},
		ProductionYear: al.Year,
		ChildCount:     len(al.Items),
		DateCreated:    embyTimeStr(al.Items[0].CreatedAt),
		LocationType:   "FileSystem",
		ImageTags:      map[string]string{},
		UserData:       &embyUserData{},
	}
	if tag := embyImageTag(&al.Items[0]); tag != "" {
		e.ImageTags["Primary"] = tag
	}
	return e
}

func (l *embyLibrary) songItem(it *model.MediaItem) *embyItem {
	name := it.ScrapedName
	if name == "" {
		name = strings.TrimSuffix(it.FileName, stdpath.Ext(it.FileName))
	}
	albumName := it.AlbumName
	if albumName == "" {
		albumName = "Unknown Album"
	}
	artist := albumArtistOf(it)
	albumKey := albumKeyOf(it)
	artistKey := artistKeyOf(it)
	e := &embyItem{
		Name:              name,
		SortName:          name,
		ServerId:          embyServerID(),
		Id:                l.guid(fmt.Sprintf("so-%d", it.ID)),
		Type:              "Audio",
		MediaType:         "Audio",
		IsFolder:          false,
		ParentId:          l.guid("al-" + albumKey),
		Album:             albumName,
		AlbumId:           l.guid("al-" + albumKey),
		AlbumArtist:       artist,
		AlbumArtists:      []embyNameID{{Name: artist, Id: l.guid("ar-" + artistKey)}},
		Artists:           []string{artist},
		ArtistItems:       []embyNameID{{Name: artist, Id: l.guid("ar-" + artistKey)}},
		IndexNumber:       it.TrackNumber,
		RunTimeTicks:      embyTicks(it.Duration),
		Path:              MediaItemFilePath(it),
		Container:         embyExt(it.FileName),
		DateCreated:       embyTimeStr(it.CreatedAt),
		CommunityRating:   it.Rating,
		UserData:          l.userDataOf(it.ID, 0),
		ImageTags:         map[string]string{},
		ProviderIds:       map[string]string{},
		LocationType:      "FileSystem",
	}
	if tag := embyImageTag(it); tag != "" {
		e.ImageTags["Primary"] = tag
	}
	return e
}

func (l *embyLibrary) songByID(id string) (*model.MediaItem, bool) {
	id = l.origID(id)
	n, err := strconv.ParseUint(strings.TrimPrefix(id, "so-"), 10, 64)
	if err != nil {
		return nil, false
	}
	for i := range l.music {
		if l.music[i].ID == uint(n) {
			return &l.music[i], true
		}
	}
	item, err := db.GetMediaItemByID(uint(n))
	if err != nil || !CanUserAccessItem(l.user, item) {
		return nil, false
	}
	return item, true
}

// ==================== 视图 / 列表 ====================

func (l *embyLibrary) viewItems() []*embyItem {
	movieCount, seriesCount := 0, 0
	for i := range l.videos {
		if isEmbySeriesItem(&l.videos[i]) {
			continue
		}
		movieCount++
	}
	seriesCount = len(l.seriesList())
	albumCount := len(l.albums())

	var out []*embyItem
	if MediaTypeEnabled(model.MediaTypeVideo) {
		out = append(out, &embyItem{
			Name: "Movies", SortName: "Movies", ServerId: embyServerID(),
			Id: l.guid("lib-movie"), ParentId: l.guid("root"), Type: "CollectionFolder", IsFolder: true,
			CollectionType: "movies", ViewType: "movies", LocationType: "FileSystem",
			ChildCount: movieCount, RecursiveItemCount: movieCount,
			// 只声明客户端能正常处理的子视图，避免生成 OpenList 没有实现的 tab
			Subviews: []string{"movies"},
			ImageTags: map[string]string{}, UserData: &embyUserData{},
		})
		out = append(out, &embyItem{
			Name: "TV Shows", SortName: "TV Shows", ServerId: embyServerID(),
			Id: l.guid("lib-tv"), ParentId: l.guid("root"), Type: "CollectionFolder", IsFolder: true,
			CollectionType: "tvshows", ViewType: "tvshows", LocationType: "FileSystem",
			ChildCount: seriesCount, RecursiveItemCount: seriesCount,
			ImageTags: map[string]string{}, UserData: &embyUserData{},
		})
	}
	if MediaTypeEnabled(model.MediaTypeMusic) {
		out = append(out, &embyItem{
			Name: "Music", SortName: "Music", ServerId: embyServerID(),
			Id: l.guid("lib-music"), ParentId: l.guid("root"), Type: "CollectionFolder", IsFolder: true,
			CollectionType: "music", ViewType: "music", LocationType: "FileSystem",
			ChildCount: albumCount, RecursiveItemCount: albumCount,
			ImageTags: map[string]string{}, UserData: &embyUserData{},
		})
	}
	return out
}

func embyViews(c *gin.Context, user *model.User) {
	lib := loadEmbyLibrary(user)
	items := lib.viewItems()
	c.JSON(200, gin.H{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0})
}

// embyHomeSections 返回服务端首页分区配置（Emby 4.10+ 客户端直接使用）。
// ListOptions.fields 必须存在：客户端会无条件调用 fields.includes()。
func embyHomeSections() []gin.H {
	return []gin.H{
		{
			"Id":              "userviews",
			"SectionType":     "userviews",
			"Name":            "Media",
			"CollectionType":  "",
			"ViewType":        "cards",
			"ScrollDirection": "Horizontal",
			"LimitItems":      false,
			"ListOptions": gin.H{
				"fields":                    []string{},
				"shape":                     "auto",
				"context":                   "home",
				"preferThumb":               false,
				"preferSeriesImage":         false,
				"showChildCountIndicator":   false,
				"image":                     nil,
				"sideFooter":                nil,
				"smallSideFooter":           nil,
				"showCurrentProgramImage":   false,
				"showAirDateTime":           false,
				"showAirEndTime":            false,
				"defaultShape":              nil,
				"action":                    nil,
				"multiSelect":               false,
				"albumFirst":                false,
				"lines":                     1,
				"focusTransformTitleAdjust": true,
				"contextMenu":               true,
				"hoverMenu":                 true,
				"hoverPlayButton":           false,
			},
			"QueryOptions": gin.H{},
		},
	}
}

// embySectionItems 返回首页某个分区的条目
func embySectionItems(user *model.User, sectionID string) gin.H {
	if sectionID == "userviews" {
		lib := loadEmbyLibrary(user)
		items := lib.viewItems()
		return gin.H{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0}
	}
	return gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0}
}

func (l *embyLibrary) rootItem() *embyItem {
	return &embyItem{
		Name: "Media", SortName: "Media", ServerId: embyServerID(),
		Id: l.guid("root"), Type: "CollectionFolder", IsFolder: true,
		LocationType: "FileSystem", ImageTags: map[string]string{}, UserData: &embyUserData{},
	}
}

// childrenOf 返回指定父级下的子项
func (l *embyLibrary) childrenOf(parentID string) []*embyItem {
	parentID = l.origID(parentID)
	switch {
	case parentID == "" || parentID == "root":
		return l.viewItems()
	case parentID == "lib-movie":
		var out []*embyItem
		for i := range l.videos {
			if !isEmbySeriesItem(&l.videos[i]) {
				out = append(out, l.movieItem(&l.videos[i]))
			}
		}
		return out
	case parentID == "lib-tv":
		var out []*embyItem
		for _, s := range l.seriesList() {
			out = append(out, l.seriesItem(s))
		}
		return out
	case parentID == "lib-music":
		var out []*embyItem
		for _, a := range l.albums() {
			out = append(out, l.albumItem(a))
		}
		return out
	}
	if strings.HasPrefix(parentID, "sr-") {
		key := strings.TrimPrefix(parentID, "sr-")
		s := l.seriesByKey(key)
		if s == nil {
			return nil
		}
		var out []*embyItem
		for _, season := range l.seasonList(s) {
			out = append(out, l.seasonItem(s, season))
		}
		return out
	}
	if strings.HasPrefix(parentID, "sn-") {
		// sn-<key>-<season>
		rest := strings.TrimPrefix(parentID, "sn-")
		idx := strings.LastIndex(rest, "-")
		if idx < 0 {
			return nil
		}
		key := rest[:idx]
		season, _ := strconv.Atoi(rest[idx+1:])
		var out []*embyItem
		for _, ep := range l.episodesOf(key, season) {
			out = append(out, l.episodeItem(ep))
		}
		return out
	}
	if strings.HasPrefix(parentID, "al-") {
		al := l.albumByKey(strings.TrimPrefix(parentID, "al-"))
		if al == nil {
			return nil
		}
		var out []*embyItem
		for i := range al.Items {
			out = append(out, l.songItem(&al.Items[i]))
		}
		return out
	}
	if strings.HasPrefix(parentID, "ar-") {
		key := strings.TrimPrefix(parentID, "ar-")
		var out []*embyItem
		for _, al := range l.albums() {
			if artistKeyOf(&al.Items[0]) == key {
				out = append(out, l.albumItem(al))
			}
		}
		return out
	}
	return nil
}

// allItems 返回媒体库全部条目（用于搜索/递归）
func (l *embyLibrary) allItems() []*embyItem {
	var out []*embyItem
	for i := range l.videos {
		if isEmbySeriesItem(&l.videos[i]) {
			continue
		}
		out = append(out, l.movieItem(&l.videos[i]))
	}
	for _, s := range l.seriesList() {
		out = append(out, l.seriesItem(s))
	}
	for i := range l.music {
		out = append(out, l.songItem(&l.music[i]))
	}
	return out
}

// embySplitCSV 解析 Emby 的逗号分隔参数，同时兼容 | 分隔
func embySplitCSV(v string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '|' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func embyInList(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// filterEmbyItems 实现 /Items 查询的通用过滤，覆盖 openemby_tv / Theater / Tsukimi 实际会发送的参数：
// IncludeItemTypes、ExcludeItemTypes、MediaTypes、Ids、IsFolder、SearchTerm、NameStartsWith、Filters
// allItemsFor 按请求的 IncludeItemTypes 只构造需要的类型。
// 客户端无 ParentId 的递归查询（如 Type=Episode / MusicAlbum）必须能返回对应类型，
// 且不应把其他媒体类型混进来。
func (l *embyLibrary) allItemsFor(include []string) []*embyItem {
	if len(include) == 0 {
		return l.allItems()
	}
	want := func(t string) bool { return embyInList(include, t) }
	var out []*embyItem
	if want("Movie") {
		for i := range l.videos {
			if !isEmbySeriesItem(&l.videos[i]) {
				out = append(out, l.movieItem(&l.videos[i]))
			}
		}
	}
	if want("Series") || want("Season") || want("Episode") {
		for _, s := range l.seriesList() {
			if want("Series") {
				out = append(out, l.seriesItem(s))
			}
			if want("Season") {
				for _, season := range l.seasonList(s) {
					out = append(out, l.seasonItem(s, season))
				}
			}
			if want("Episode") {
				for _, ep := range l.episodesOf(s.Key, 0) {
					out = append(out, l.episodeItem(ep))
				}
			}
		}
	}
	if want("Audio") {
		for i := range l.music {
			out = append(out, l.songItem(&l.music[i]))
		}
	}
	if want("MusicAlbum") {
		for _, a := range l.albums() {
			out = append(out, l.albumItem(a))
		}
	}
	return out
}

func filterEmbyItems(items []*embyItem, c *gin.Context) []*embyItem {
	include := embySplitCSV(c.Query("IncludeItemTypes"))
	exclude := embySplitCSV(c.Query("ExcludeItemTypes"))
	mediaTypes := embySplitCSV(c.Query("MediaTypes"))
	searchTerm := strings.ToLower(strings.TrimSpace(c.Query("SearchTerm")))
	namePrefix := strings.ToLower(strings.TrimSpace(c.Query("NameStartsWith")))
	filters := strings.ToLower(c.Query("Filters"))
	isFolder := c.Query("IsFolder")
	idSet := map[string]bool{}
	for _, id := range embySplitCSV(c.Query("Ids")) {
		idSet[strings.ToLower(id)] = true
	}

	var out []*embyItem
	for _, it := range items {
		if len(include) > 0 && !embyInList(include, it.Type) {
			continue
		}
		if len(exclude) > 0 && embyInList(exclude, it.Type) {
			continue
		}
		if len(mediaTypes) > 0 && !embyInList(mediaTypes, it.MediaType) {
			continue
		}
		if len(idSet) > 0 && !idSet[strings.ToLower(it.Id)] {
			continue
		}
		lname := strings.ToLower(it.Name)
		if searchTerm != "" && !strings.Contains(lname, searchTerm) {
			continue
		}
		if namePrefix != "" && !strings.HasPrefix(lname, namePrefix) {
			continue
		}
		if isFolder != "" {
			want := strings.EqualFold(isFolder, "true")
			if it.IsFolder != want {
				continue
			}
		}
		if strings.Contains(filters, "isfavorite") {
			if it.UserData == nil || !it.UserData.IsFavorite {
				continue
			}
		}
		if strings.Contains(filters, "isunplayed") {
			if it.UserData != nil && it.UserData.Played {
				continue
			}
		} else if strings.Contains(filters, "isplayed") {
			if it.UserData == nil || !it.UserData.Played {
				continue
			}
		}
		out = append(out, it)
	}
	return out
}

func embyTypeMatches(itemType, wanted string) bool {
	return embyInList(embySplitCSV(wanted), itemType)
}

// sortEmbyItems 支持多个排序键（SortBy=SeriesSortName,ParentIndexNumber,IndexNumber,SortName）
func sortEmbyItems(items []*embyItem, sortBy, order string) {
	if sortBy == "" {
		return
	}
	keys := embySplitCSV(sortBy)
	desc := strings.EqualFold(order, "Descending")
	if len(keys) > 0 && strings.EqualFold(keys[0], "random") {
		for i := len(items) - 1; i > 0; i-- {
			j := int(timeNowUnixNano() % int64(i+1))
			items[i], items[j] = items[j], items[i]
		}
		return
	}
	cmp := func(a, b *embyItem, key string) int {
		switch strings.ToLower(key) {
		case "sortname", "name", "seriessortname":
			la, lb := strings.ToLower(a.SortName), strings.ToLower(b.SortName)
			if strings.EqualFold(key, "seriessortname") {
				la, lb = strings.ToLower(a.SeriesName), strings.ToLower(b.SeriesName)
			}
			switch {
			case la < lb:
				return -1
			case la > lb:
				return 1
			}
		case "datecreated", "dateplayed":
			switch {
			case a.DateCreated < b.DateCreated:
				return -1
			case a.DateCreated > b.DateCreated:
				return 1
			}
		case "productionyear":
			return a.ProductionYear - b.ProductionYear
		case "parentindexnumber":
			return a.ParentIndexNumber - b.ParentIndexNumber
		case "indexnumber":
			return a.IndexNumber - b.IndexNumber
		case "communityrating":
			switch {
			case a.CommunityRating < b.CommunityRating:
				return -1
			case a.CommunityRating > b.CommunityRating:
				return 1
			}
		}
		return 0
	}
	sort.SliceStable(items, func(i, j int) bool {
		for _, k := range keys {
			if r := cmp(items[i], items[j], k); r != 0 {
				if desc {
					return r > 0
				}
				return r < 0
			}
		}
		return false
	})
}

func embyPaginate(items []*embyItem, start, limit int) []*embyItem {
	if start >= len(items) {
		return []*embyItem{}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func timeNowUnixNano() int64 {
	return time.Now().UnixNano()
}

// ==================== 条目分发 ====================

// embyUserScoped 处理 /Users/{uid}/... 下的条目相关请求
func embyUserScoped(c *gin.Context, user *model.User, segs, lower []string, method string) {
	// 服务端首页分区（Emby 4.10+ 客户端）
	if len(lower) >= 3 && lower[2] == "homesections" {
		if method == "GET" && len(lower) == 3 {
			c.JSON(200, embyHomeSections())
		} else {
			c.Status(204)
		}
		return
	}
	// 首页分区条目：/Users/{uid}/Sections/{sectionId}/Items
	if len(lower) == 5 && lower[2] == "sections" && lower[4] == "items" {
		c.JSON(200, embySectionItems(user, lower[3]))
		return
	}
	// /users/{uid}/items...
	if len(lower) >= 3 && lower[2] == "items" {
		lib := loadEmbyLibrary(user)
		switch {
		case len(lower) == 3 && (method == "GET" || method == "POST"):
			// /Users/{uid}/Items?ParentId=...&Recursive=...&IncludeItemTypes=...
			// 必须按查询条件返回真实数据；Playlist/BoxSet 等无对应类型会被过滤为空
			embyItemList(c, lib, c.Query("ParentId"))
		case len(lower) == 4 && lower[3] == "root":
			c.JSON(200, lib.rootItem())
		case len(lower) == 4 && lower[3] == "latest":
			embyLatest(c, lib)
		case len(lower) == 4 && lower[3] == "resume":
			embyResume(c, lib)
		case len(lower) == 4 && method == "GET":
			// 单条目
			embyGetItem(c, lib, segs[3])
		case len(lower) == 5 && lower[4] == "children":
			embyChildren(c, lib, segs[3])
		case len(lower) == 5 && lower[4] == "playbackinfo":
			embyPlaybackInfo(c, lib, segs[3])
		case len(lower) == 5 && lower[4] == "userdata":
			embyUpdateUserData(c, user, segs[3])
		case len(lower) == 4:
			embyItemList(c, lib, segs[3])
		case len(lower) == 5 && embyAuxItem(c, lower[4]):
		default:
			embyNotFound(c)
		}
		return
	}
	if len(lower) == 3 && embyAuxItem(c, lower[2]) {
		return
	}
	// /users/{uid}/settings：客户端保存/读取首页与播放设置
	if len(lower) == 3 && lower[2] == "settings" {
		if method == "GET" {
			c.JSON(200, gin.H{"HomeSections": []any{}})
		} else {
			c.Status(204)
		}
		return
	}
	// /users/{uid}/views 已在外部处理
	if len(lower) >= 3 && (lower[2] == "playeditems" || lower[2] == "favoriteitems" || lower[2] == "playingitems") {
		if len(lower) < 4 {
			embyNotFound(c)
			return
		}
		kind := lower[2]
		id := segs[3]
		action := ""
		if len(lower) == 5 {
			action = lower[4]
		}
		switch kind {
		case "playeditems":
			played := method == "POST" && action != "delete"
			embySetPlayed(c, user, id, played)
		case "favoriteitems":
			fav := method == "POST" && action != "delete"
			embySetFavorite(c, user, id, fav)
		case "playingitems":
			if len(lower) == 5 && lower[4] == "progress" {
				embySaveProgress(c, user, id, false)
				return
			}
			embySaveProgress(c, user, id, true)
		}
		return
	}
	embyNotFound(c)
}

// embyItemsScoped 处理 /Items/... 全局请求
func embyItemsScoped(c *gin.Context, user *model.User, segs, lower []string, method string) {
	lib := loadEmbyLibrary(user)
	switch {
	case len(lower) == 2 && lower[1] == "counts":
		embyItemCounts(c, user)
	case len(lower) == 2 && method == "GET":
		embyGetItem(c, lib, segs[1])
	case len(lower) == 3 && lower[2] == "children":
		embyChildren(c, lib, segs[1])
	case len(lower) == 3 && lower[2] == "playbackinfo":
		embyPlaybackInfo(c, lib, segs[1])
	case len(lower) == 3 && lower[2] == "refresh" && method == "POST":
		embyRefreshItem(c, lib.origID(segs[1]))
	// 歌词：/Items/{id}/{mediaSourceId}/Subtitles/{index}/Stream.js
	case len(lower) >= 4 && lower[3] == "subtitles":
		embySubtitleStream(c, user, segs[1])
	case len(lower) >= 3 && lower[2] == "subtitles":
		embySubtitleStream(c, user, segs[1])
	case len(lower) >= 3 && lower[2] == "images":
		embyImage(c, segs[1], segs, true)
	case len(lower) == 3 && embyAuxItem(c, lower[2]):
	default:
		embyNotFound(c)
	}
}

func embyGetItem(c *gin.Context, lib *embyLibrary, id string) {
	item, ok := lib.resolveItem(id)
	if !ok {
		embyNotFound(c)
		return
	}
	// 有歌词的歌曲在详情里也带上媒体源：客户端歌词面板读取
	// item.MediaSources[0].DefaultSubtitleStreamIndex 来定位歌词轨
	if item.Type == "Audio" {
		if song, ok := lib.songByID(lib.origID(id)); ok && song.Lyrics != "" {
			idx := 1
			item.MediaSources = []embyMediaSource{{
				Protocol: "Http", Id: item.Id, ItemId: item.Id, Type: "Default",
				SupportsDirectPlay: true, SupportsDirectStream: true,
				MediaStreams: []embyMediaStream{
					{Type: "Audio", Index: 0, IsDefault: true},
					embyLyricsMediaStream(idx),
				},
				MediaAttachments: []any{}, Formats: []string{},
				RequiredHttpHeaders:        map[string]string{},
				DefaultSubtitleStreamIndex: &idx,
			}}
		}
	}
	c.JSON(200, item)
}

func (l *embyLibrary) resolveItem(id string) (*embyItem, bool) {
	id = l.origID(id)
	switch {
	case strings.HasPrefix(id, "mv-"):
		n, err := strconv.ParseUint(strings.TrimPrefix(id, "mv-"), 10, 64)
		if err != nil {
			return nil, false
		}
		for i := range l.videos {
			if l.videos[i].ID == uint(n) {
				return l.movieItem(&l.videos[i]), true
			}
		}
	case strings.HasPrefix(id, "so-"):
		if it, ok := l.songByID(id); ok {
			return l.songItem(it), true
		}
	case strings.HasPrefix(id, "sr-"):
		if s := l.seriesByKey(strings.TrimPrefix(id, "sr-")); s != nil {
			return l.seriesItem(s), true
		}
	case strings.HasPrefix(id, "sn-"):
		rest := strings.TrimPrefix(id, "sn-")
		idx := strings.LastIndex(rest, "-")
		if idx > 0 {
			key := rest[:idx]
			season, _ := strconv.Atoi(rest[idx+1:])
			if s := l.seriesByKey(key); s != nil {
				return l.seasonItem(s, season), true
			}
		}
	case strings.HasPrefix(id, "al-"):
		if al := l.albumByKey(strings.TrimPrefix(id, "al-")); al != nil {
			return l.albumItem(al), true
		}
	case strings.HasPrefix(id, "ep-"):
		if ep := l.findEpisode(id); ep != nil {
			return l.episodeItem(ep), true
		}
	case strings.HasPrefix(id, "ar-"):
		key := strings.TrimPrefix(id, "ar-")
		for i := range l.music {
			if artistKeyOf(&l.music[i]) != key {
				continue
			}
			name := albumArtistOf(&l.music[i])
			e := &embyItem{
				Name: name, SortName: name, ServerId: embyServerID(),
				Id: l.guid("ar-" + key), ParentId: l.guid("lib-music"),
				Type: "MusicArtist", IsFolder: true,
				ImageTags: map[string]string{}, UserData: &embyUserData{},
				LocationType: "FileSystem",
			}
			if tag := embyImageTag(&l.music[i]); tag != "" {
				e.ImageTags["Primary"] = tag
			}
			return e, true
		}
	case strings.HasPrefix(id, "lib-") || id == "root":
		for _, v := range l.viewItems() {
			if strings.EqualFold(v.Id, l.guid(id)) {
				return v, true
			}
		}
		if id == "root" {
			return l.rootItem(), true
		}
	}
	return nil, false
}

// descendantsOf 递归展开 parentID 下的全部后代条目。
// 递归必须以 ParentId 为范围：此前直接返回全库条目，导致电影/剧集/音乐库互相串内容。
func (l *embyLibrary) descendantsOf(parentID string) []*embyItem {
	var out []*embyItem
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		if depth > 6 {
			return
		}
		for _, ch := range l.childrenOf(id) {
			out = append(out, ch)
			if ch.IsFolder {
				walk(ch.Id, depth+1)
			}
		}
	}
	walk(parentID, 0)
	return out
}

func embyItemList(c *gin.Context, lib *embyLibrary, parentID string) {
	if q := strings.TrimSpace(c.Query("ParentId")); q != "" {
		parentID = q
	}
	var items []*embyItem
	if embyBoolQuery(c, "Recursive") {
		if lib.origID(parentID) == "" || lib.origID(parentID) == "root" {
			items = lib.allItemsFor(embySplitCSV(c.Query("IncludeItemTypes")))
		} else {
			items = lib.descendantsOf(parentID)
		}
	} else {
		items = lib.childrenOf(parentID)
	}
	items = filterEmbyItems(items, c)
	sortEmbyItems(items, c.Query("SortBy"), c.Query("SortOrder"))
	total := len(items)
	start, limit := embyPaging(c)
	items = embyPaginate(items, start, limit)
	c.JSON(200, embyQueryResult{Items: items, TotalRecordCount: total, StartIndex: start})
}

func embyChildren(c *gin.Context, lib *embyLibrary, parentID string) {
	items := lib.childrenOf(parentID)
	items = filterEmbyItems(items, c)
	start, limit := embyPaging(c)
	total := len(items)
	c.JSON(200, embyQueryResult{Items: embyPaginate(items, start, limit), TotalRecordCount: total, StartIndex: start})
}

func embyLatest(c *gin.Context, lib *embyLibrary) {
	// 客户端按媒体库分别请求"最近添加"：带 ParentId 时只返回该库内容，避免电影/电视剧/音乐互相串
	if pid := lib.origID(c.Query("ParentId")); pid == "lib-movie" || pid == "lib-tv" || pid == "lib-music" {
		items := filterEmbyItems(lib.childrenOf(pid), c)
		sort.SliceStable(items, func(i, j int) bool { return items[i].DateCreated > items[j].DateCreated })
		_, limit := embyPaging(c)
		c.JSON(200, embyPaginate(items, 0, limit))
		return
	}
	var items []*embyItem
	if MediaTypeEnabled(model.MediaTypeVideo) {
		var movies []model.MediaItem
		for i := range lib.videos {
			if !isEmbySeriesItem(&lib.videos[i]) {
				movies = append(movies, lib.videos[i])
			}
		}
		sort.Slice(movies, func(i, j int) bool { return movies[i].CreatedAt.After(movies[j].CreatedAt) })
		for i := range movies {
			items = append(items, lib.movieItem(&movies[i]))
			if len(items) >= 50 {
				break
			}
		}
	}
	if MediaTypeEnabled(model.MediaTypeMusic) {
		sort.Slice(lib.music, func(i, j int) bool { return lib.music[i].CreatedAt.After(lib.music[j].CreatedAt) })
		for i := range lib.music {
			items = append(items, lib.songItem(&lib.music[i]))
			if len(items) >= 100 {
				break
			}
		}
	}
	_, limit := embyPaging(c)
	c.JSON(200, embyPaginate(items, 0, limit))
}

func embyResume(c *gin.Context, lib *embyLibrary) {
	if lib.user == nil {
		c.JSON(200, []*embyItem{})
		return
	}
	rows, err := db.ListMediaResume(lib.user.ID, 100)
	if err != nil {
		c.JSON(200, []*embyItem{})
		return
	}
	var items []*embyItem
	for _, row := range rows {
		id := fmt.Sprintf("mv-%d", row.ItemID)
		if row.EpisodeIndex > 0 {
			id = fmt.Sprintf("ep-%d-%d", row.ItemID, row.EpisodeIndex)
		}
		if it, ok := lib.resolveItem(id); ok {
			if it.UserData == nil {
				it.UserData = &embyUserData{}
			}
			it.UserData.PlaybackPositionTicks = row.PositionTicks
			items = append(items, it)
		}
	}
	c.JSON(200, items)
}

func embyItemCounts(c *gin.Context, user *model.User) {
	lib := loadEmbyLibrary(user)
	movies, series, episodes, songs, albums := 0, 0, 0, 0, 0
	for i := range lib.videos {
		if isEmbySeriesItem(&lib.videos[i]) {
			continue
		}
		movies++
	}
	for range lib.seriesList() {
		series++
	}
	for _, s := range lib.seriesList() {
		episodes += len(lib.episodesOf(s.Key, 0))
	}
	songs = len(lib.music)
	albums = len(lib.albums())
	c.JSON(200, gin.H{
		"MovieCount": movies, "SeriesCount": series, "EpisodeCount": episodes,
		"SongCount": songs, "AlbumCount": albums,
		"ArtistCount": len(embyArtistKeys(lib)), "BookCount": 0, "TrailerCount": 0,
	})
}

// ==================== 剧集接口 ====================

func embyShows(c *gin.Context, user *model.User, segs, lower []string, method string) {
	lib := loadEmbyLibrary(user)
	// /Shows/NextUp 只有两段路径
	if len(lower) == 2 && lower[1] == "nextup" {
		embyNextUp(c, lib)
		return
	}
	// /Shows/{id} 返回剧集详情
	if len(lower) == 2 {
		if item, ok := lib.resolveItem(segs[1]); ok {
			c.JSON(200, item)
			return
		}
		embyNotFound(c)
		return
	}
	seriesID := lib.origID(segs[1])
	key := strings.TrimPrefix(seriesID, "sr-")
	s := lib.seriesByKey(key)
	if s == nil {
		embyNotFound(c)
		return
	}
	switch lower[2] {
	case "seasons":
		var items []*embyItem
		for _, season := range lib.seasonList(s) {
			items = append(items, lib.seasonItem(s, season))
		}
		c.JSON(200, gin.H{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0})
	case "episodes":
		season := embyIntQuery(c, "Season")
		// 客户端也会传 SeasonId（GUID）按季过滤
		if sid := lib.origID(c.Query("SeasonId")); strings.HasPrefix(sid, "sn-") {
			if idx := strings.LastIndex(sid, "-"); idx > 0 {
				season, _ = strconv.Atoi(sid[idx+1:])
			}
		}
		var items []*embyItem
		for _, ep := range lib.episodesOf(key, season) {
			items = append(items, lib.episodeItem(ep))
		}
		c.JSON(200, gin.H{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0})
	default:
		embyNotFound(c)
	}
}

func embyNextUp(c *gin.Context, lib *embyLibrary) {
	var items []*embyItem
	seriesFilter := strings.TrimPrefix(lib.origID(c.Query("SeriesId")), "sr-")
	for _, s := range lib.seriesList() {
		if seriesFilter != "" && s.Key != seriesFilter {
			continue
		}
		if lib.user != nil {
			if rows, err := db.ListMediaResume(lib.user.ID, 200); err == nil {
				nextIdx := -1
				for _, row := range rows {
					if seriesKeyOf(&model.MediaItem{ScrapedName: s.Name}) == s.Key &&
						row.ItemID == s.Items[0].ID && row.EpisodeIndex > 0 {
						nextIdx = row.EpisodeIndex + 1
						break
					}
				}
				eps := lib.episodesOf(s.Key, 0)
				if nextIdx > 0 {
					for _, ep := range eps {
						if ep.Index == nextIdx {
							items = append(items, lib.episodeItem(ep))
							break
						}
					}
				} else if len(eps) > 0 {
					items = append(items, lib.episodeItem(eps[0]))
				}
				continue
			}
		}
		eps := lib.episodesOf(s.Key, 0)
		if len(eps) > 0 {
			items = append(items, lib.episodeItem(eps[0]))
		}
	}
	c.JSON(200, gin.H{"Items": items, "TotalRecordCount": len(items)})
}

// ==================== 音乐 / 搜索 ====================

func embyArtistKeys(lib *embyLibrary) []string {
	set := map[string]string{}
	for i := range lib.music {
		key := artistKeyOf(&lib.music[i])
		set[key] = albumArtistOf(&lib.music[i])
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func embyArtists(c *gin.Context, user *model.User, albumOnly bool) {
	lib := loadEmbyLibrary(user)
	seen := map[string]bool{}
	var items []*embyItem
	for i := range lib.music {
		it := &lib.music[i]
		key := artistKeyOf(it)
		if seen[key] {
			continue
		}
		seen[key] = true
		name := albumArtistOf(it)
		e := &embyItem{
			Name: name, SortName: name, ServerId: embyServerID(),
			Id: lib.guid("ar-" + key), ParentId: lib.guid("lib-music"), Type: "MusicArtist", IsFolder: true,
			ImageTags: map[string]string{}, UserData: &embyUserData{},
			LocationType: "FileSystem",
		}
		if tag := embyImageTag(it); tag != "" {
			e.ImageTags["Primary"] = tag
		}
		items = append(items, e)
	}
	start, limit := embyPaging(c)
	c.JSON(200, embyQueryResult{Items: embyPaginate(items, start, limit), TotalRecordCount: len(items), StartIndex: start})
}

func embySearchHints(c *gin.Context, user *model.User) {
	lib := loadEmbyLibrary(user)
	term := strings.ToLower(strings.TrimSpace(c.Query("SearchTerm")))
	items := lib.allItems()
	items = filterEmbyItems(items, c)
	var hints []gin.H
	for _, it := range items {
		if term != "" && !strings.Contains(strings.ToLower(it.Name), term) {
			continue
		}
		hints = append(hints, gin.H{
			"ItemId":         it.Id,
			"Id":             it.Id,
			"Name":           it.Name,
			"Type":           it.Type,
			"MediaType":      it.MediaType,
			"RunTimeTicks":   it.RunTimeTicks,
			"ProductionYear": it.ProductionYear,
			"ImageTags":      it.ImageTags,
		})
		if len(hints) >= 50 {
			break
		}
	}
	c.JSON(200, gin.H{"SearchHints": hints, "TotalRecordCount": len(hints)})
}

// ==================== 媒体库目录信息（MoviePilot 需要） ====================

func embySelectableFolders(c *gin.Context, user *model.User) {
	paths, err := db.ListMediaScanPaths("")
	if err != nil {
		c.JSON(500, gin.H{"Error": err.Error()})
		return
	}
	var out []gin.H
	for _, p := range paths {
		if !CanUserAccessPath(user, p.Path) {
			continue
		}
		out = append(out, gin.H{
			"Name": p.Name,
			"SubFolders": []gin.H{{
				"Name": p.Name,
				"Path": p.Path,
				"Id":   strconv.FormatUint(uint64(p.ID), 10),
			}},
		})
	}
	c.JSON(200, out)
}

func embyVirtualFolders(c *gin.Context, user *model.User) {
	paths, err := db.ListMediaScanPaths("")
	if err != nil {
		c.JSON(500, gin.H{"Error": err.Error()})
		return
	}
	var out []gin.H
	for _, p := range paths {
		if !CanUserAccessPath(user, p.Path) {
			continue
		}
		collectionType := "movies"
		switch p.MediaType {
		case model.MediaTypeVideo:
			if p.TypeTag == "电视剧" || p.TypeTag == "tv" {
				collectionType = "tvshows"
			}
		case model.MediaTypeMusic:
			collectionType = "music"
		case model.MediaTypeImage:
			collectionType = "homevideos"
		case model.MediaTypeBook:
			collectionType = "books"
		}
		out = append(out, gin.H{
			"Name": p.Name,
			"ItemId": "libpath-" + strconv.FormatUint(uint64(p.ID), 10),
			"CollectionType": collectionType,
			"Locations": []string{p.Path},
			"LibraryOptions": gin.H{
				"PathInfos": []gin.H{{"Path": p.Path}},
			},
		})
	}
	c.JSON(200, out)
}
