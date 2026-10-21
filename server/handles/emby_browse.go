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
}

func loadEmbyLibrary(user *model.User) *embyLibrary {
	l := &embyLibrary{user: user, userData: map[[2]uint]*model.MediaUserData{}}
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
	return l
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
		if it.VideoType != "tv" {
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
		if it.VideoType != "tv" || seriesKeyOf(it) != seriesKey {
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
		Id:              fmt.Sprintf("mv-%d", it.ID),
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
		Id:           "sr-" + s.Key,
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
	return &embyItem{
		Name:         fmt.Sprintf("Season %d", season),
		SortName:     fmt.Sprintf("Season %02d", season),
		ServerId:     embyServerID(),
		Id:           fmt.Sprintf("sn-%s-%d", s.Key, season),
		Type:         "Season",
		IsFolder:     true,
		ParentId:     "sr-" + s.Key,
		SeriesId:     "sr-" + s.Key,
		SeriesName:   s.Name,
		IndexNumber:  season,
		ChildCount:   len(eps),
		LocationType: "FileSystem",
		ImageTags:    map[string]string{},
		UserData:     &embyUserData{UnplayedItemCount: unplayed},
	}
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
		Id:                ep.ID,
		Type:              "Episode",
		MediaType:         "Video",
		IsFolder:          false,
		ParentId:          seasonID,
		SeriesId:          seriesID,
		SeriesName:        ep.SeriesName,
		SeasonId:          seasonID,
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
		Id:             "al-" + al.Key,
		Type:           "MusicAlbum",
		IsFolder:       true,
		AlbumArtist:    al.Artist,
		AlbumArtists:   []embyNameID{{Name: al.Artist, Id: "ar-" + artistKeyOf(&al.Items[0])}},
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
		Id:                fmt.Sprintf("so-%d", it.ID),
		Type:              "Audio",
		MediaType:         "Audio",
		IsFolder:          false,
		ParentId:          "al-" + albumKey,
		Album:             albumName,
		AlbumId:           "al-" + albumKey,
		AlbumArtist:       artist,
		AlbumArtists:      []embyNameID{{Name: artist, Id: "ar-" + artistKey}},
		Artists:           []string{artist},
		ArtistItems:       []embyNameID{{Name: artist, Id: "ar-" + artistKey}},
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
	var out []*embyItem
	if MediaTypeEnabled(model.MediaTypeVideo) {
		out = append(out, &embyItem{
			Name: "Movies", SortName: "Movies", ServerId: embyServerID(),
			Id: "lib-movie", Type: "CollectionFolder", IsFolder: true,
			CollectionType: "movies", LocationType: "FileSystem",
			ImageTags: map[string]string{}, UserData: &embyUserData{},
		})
		out = append(out, &embyItem{
			Name: "TV Shows", SortName: "TV Shows", ServerId: embyServerID(),
			Id: "lib-tv", Type: "CollectionFolder", IsFolder: true,
			CollectionType: "tvshows", LocationType: "FileSystem",
			ImageTags: map[string]string{}, UserData: &embyUserData{},
		})
	}
	if MediaTypeEnabled(model.MediaTypeMusic) {
		out = append(out, &embyItem{
			Name: "Music", SortName: "Music", ServerId: embyServerID(),
			Id: "lib-music", Type: "CollectionFolder", IsFolder: true,
			CollectionType: "music", LocationType: "FileSystem",
			ImageTags: map[string]string{}, UserData: &embyUserData{},
		})
	}
	return out
}

func embyViews(c *gin.Context, user *model.User) {
	lib := loadEmbyLibrary(user)
	c.JSON(200, gin.H{"Items": lib.viewItems(), "TotalRecordCount": len(lib.viewItems())})
}

func (l *embyLibrary) rootItem() *embyItem {
	return &embyItem{
		Name: "Media", SortName: "Media", ServerId: embyServerID(),
		Id: "root", Type: "CollectionFolder", IsFolder: true,
		LocationType: "FileSystem", ImageTags: map[string]string{}, UserData: &embyUserData{},
	}
}

// childrenOf 返回指定父级下的子项
func (l *embyLibrary) childrenOf(parentID string) []*embyItem {
	parentID = strings.TrimSpace(parentID)
	switch {
	case parentID == "" || parentID == "root":
		return l.viewItems()
	case parentID == "lib-movie":
		var out []*embyItem
		for i := range l.videos {
			if l.videos[i].VideoType != "tv" {
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
		if l.videos[i].VideoType == "tv" {
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

func filterEmbyItems(items []*embyItem, c *gin.Context) []*embyItem {
	types := strings.ToLower(c.Query("IncludeItemTypes"))
	searchTerm := strings.ToLower(strings.TrimSpace(c.Query("SearchTerm")))
	filters := strings.ToLower(c.Query("Filters"))
	isFolder := c.Query("IsFolder")

	var out []*embyItem
	for _, it := range items {
		if types != "" && !embyTypeMatches(it.Type, types) {
			continue
		}
		if searchTerm != "" && !strings.Contains(strings.ToLower(it.Name), searchTerm) {
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
		if strings.Contains(filters, "isplayed") {
			if it.UserData == nil || !it.UserData.Played {
				continue
			}
		}
		if strings.Contains(filters, "isunplayed") {
			if it.UserData != nil && it.UserData.Played {
				continue
			}
		}
		out = append(out, it)
	}
	return out
}

func embyTypeMatches(itemType, wanted string) bool {
	for _, t := range strings.Split(wanted, ",") {
		if strings.EqualFold(strings.TrimSpace(t), itemType) {
			return true
		}
	}
	return false
}

func sortEmbyItems(items []*embyItem, sortBy, order string) {
	if sortBy == "" {
		return
	}
	desc := strings.EqualFold(order, "Descending")
	switch strings.ToLower(strings.Split(sortBy, ",")[0]) {
	case "sortname", "name":
		sort.SliceStable(items, func(i, j int) bool {
			if desc {
				return items[i].SortName > items[j].SortName
			}
			return items[i].SortName < items[j].SortName
		})
	case "datecreated":
		sort.SliceStable(items, func(i, j int) bool {
			if desc {
				return items[i].DateCreated > items[j].DateCreated
			}
			return items[i].DateCreated < items[j].DateCreated
		})
	case "productionyear":
		sort.SliceStable(items, func(i, j int) bool {
			if desc {
				return items[i].ProductionYear > items[j].ProductionYear
			}
			return items[i].ProductionYear < items[j].ProductionYear
		})
	case "random":
		for i := len(items) - 1; i > 0; i-- {
			j := int(timeNowUnixNano() % int64(i+1))
			items[i], items[j] = items[j], items[i]
		}
	}
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
	// /users/{uid}/items...
	if len(lower) >= 3 && lower[2] == "items" {
		lib := loadEmbyLibrary(user)
		switch {
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
		default:
			embyNotFound(c)
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
		embyRefreshItem(c, segs[1])
	case len(lower) >= 3 && lower[2] == "images":
		embyImage(c, segs[1], segs, true)
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
	c.JSON(200, item)
}

func (l *embyLibrary) resolveItem(id string) (*embyItem, bool) {
	id = strings.TrimSpace(id)
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
	case strings.HasPrefix(id, "lib-") || id == "root":
		for _, v := range l.viewItems() {
			if strings.EqualFold(v.Id, id) {
				return v, true
			}
		}
		if id == "root" {
			return l.rootItem(), true
		}
	}
	return nil, false
}

func embyItemList(c *gin.Context, lib *embyLibrary, parentID string) {
	items := lib.childrenOf(parentID)
	if embyBoolQuery(c, "Recursive") {
		items = lib.allItems()
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
	var items []*embyItem
	if MediaTypeEnabled(model.MediaTypeVideo) {
		var movies []model.MediaItem
		for i := range lib.videos {
			if lib.videos[i].VideoType != "tv" {
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
		if lib.videos[i].VideoType == "tv" {
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
	if len(lower) == 3 && lower[1] == "nextup" {
		embyNextUp(c, lib)
		return
	}
	if len(lower) < 3 {
		embyNotFound(c)
		return
	}
	seriesID := segs[1]
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
		c.JSON(200, gin.H{"Items": items, "TotalRecordCount": len(items)})
	case "episodes":
		season := embyIntQuery(c, "Season")
		var items []*embyItem
		for _, ep := range lib.episodesOf(key, season) {
			items = append(items, lib.episodeItem(ep))
		}
		c.JSON(200, gin.H{"Items": items, "TotalRecordCount": len(items)})
	default:
		embyNotFound(c)
	}
}

func embyNextUp(c *gin.Context, lib *embyLibrary) {
	var items []*embyItem
	seriesFilter := strings.TrimPrefix(c.Query("SeriesId"), "sr-")
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
			Id: "ar-" + key, Type: "MusicArtist", IsFolder: true,
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
