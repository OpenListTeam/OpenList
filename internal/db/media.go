package db

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/gorm"
)

// ==================== MediaConfig ====================

// GetMediaConfig 获取指定类型的媒体库配置，不存在则返回默认值
func GetMediaConfig(mediaType model.MediaType) (*model.MediaConfig, error) {
	var cfg model.MediaConfig
	result := db.Where("media_type = ?", mediaType).First(&cfg)
	if result.Error == gorm.ErrRecordNotFound {
		return &model.MediaConfig{
			MediaType: mediaType,
			Enabled:   false,
		}, nil
	}
	return &cfg, result.Error
}

// GetAllMediaConfigs 获取所有媒体库配置
func GetAllMediaConfigs() ([]model.MediaConfig, error) {
	var cfgs []model.MediaConfig
	err := db.Find(&cfgs).Error
	return cfgs, err
}

// SaveMediaConfig 保存媒体库配置（upsert）
func SaveMediaConfig(cfg *model.MediaConfig) error {
	var existing model.MediaConfig
	result := db.Where("media_type = ?", cfg.MediaType).First(&existing)
	if result.Error == gorm.ErrRecordNotFound {
		return db.Create(cfg).Error
	}
	cfg.ID = existing.ID
	return db.Save(cfg).Error
}

// ==================== MediaScanPath ====================

// ListMediaScanPaths 获取指定媒体类型的所有扫描路径
func ListMediaScanPaths(mediaType model.MediaType) ([]model.MediaScanPath, error) {
	var paths []model.MediaScanPath
	tx := db.Model(&model.MediaScanPath{})
	if mediaType != "" {
		tx = tx.Where("media_type = ?", mediaType)
	}
	err := tx.Order("id asc").Find(&paths).Error
	return paths, err
}

// GetMediaScanPath 按ID获取扫描路径
func GetMediaScanPath(id uint) (*model.MediaScanPath, error) {
	var p model.MediaScanPath
	err := db.First(&p, id).Error
	return &p, err
}

// CreateMediaScanPath 创建扫描路径
func CreateMediaScanPath(p *model.MediaScanPath) error {
	return db.Create(p).Error
}

// UpdateMediaScanPath 更新扫描路径
func UpdateMediaScanPath(p *model.MediaScanPath) error {
	return db.Save(p).Error
}

// DeleteMediaScanPath 删除扫描路径（硬删除）
func DeleteMediaScanPath(id uint) error {
	return db.Unscoped().Delete(&model.MediaScanPath{}, id).Error
}

// ==================== MediaItem ====================

// MediaItemQuery 媒体条目查询参数
type MediaItemQuery struct {
	MediaType  model.MediaType
	ScanPathID uint   // 按扫描路径ID筛选
	FolderPath string // 按文件夹路径筛选
	TypeTag    string // 按类型标签筛选（电影、电视剧等）
	ContentTag string // 按内容标签筛选（喜剧、惊悚等）
	Hidden     *bool
	Keyword    string
	OrderBy    string // "name", "date", "size"
	OrderDir   string // "asc", "desc"
	Page       int
	PageSize   int
}

// ListMediaItems 分页查询媒体条目
func ListMediaItems(q MediaItemQuery) ([]model.MediaItem, int64, error) {
	tx := db.Model(&model.MediaItem{})
	if q.MediaType != "" {
		tx = tx.Where("media_type = ?", q.MediaType)
	}
	if q.ScanPathID > 0 {
		tx = tx.Where("scan_path_id = ?", q.ScanPathID)
	}
	if q.FolderPath != "" {
		tx = tx.Where("folder_path = ?", q.FolderPath)
	}
	if q.Hidden != nil {
		tx = tx.Where("hidden = ?", *q.Hidden)
	}
	if q.Keyword != "" {
		like := "%" + q.Keyword + "%"
		tx = tx.Where("file_name LIKE ? OR scraped_name LIKE ?", like, like)
	}
	// 按类型标签筛选（通过关联扫描路径的type_tag）
	if q.TypeTag != "" {
		tx = tx.Joins("JOIN media_scan_paths ON media_scan_paths.id = media_items.scan_path_id").
			Where("media_scan_paths.type_tag = ?", q.TypeTag)
	}
	// 按内容标签筛选（通过关联扫描路径的content_tags）
	if q.ContentTag != "" {
		tx = tx.Joins("JOIN media_scan_paths ON media_scan_paths.id = media_items.scan_path_id").
			Where("media_scan_paths.content_tags LIKE ?", "%"+q.ContentTag+"%")
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 排序
	orderCol := "created_at"
	switch q.OrderBy {
	case "name":
		orderCol = "COALESCE(NULLIF(scraped_name,''), file_name)"
	case "date":
		orderCol = "release_date"
	case "size":
		orderCol = "file_size"
	}
	dir := "asc"
	if q.OrderDir == "desc" {
		dir = "desc"
	}
	tx = tx.Order(orderCol + " " + dir)

	// 分页
	if q.PageSize <= 0 {
		q.PageSize = 20
	}
	if q.Page <= 0 {
		q.Page = 1
	}
	tx = tx.Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize)

	var items []model.MediaItem
	err := tx.Find(&items).Error
	return items, total, err
}

// GetMediaItemByID 按ID获取媒体条目
func GetMediaItemByID(id uint) (*model.MediaItem, error) {
	var item model.MediaItem
	err := db.First(&item, id).Error
	return &item, err
}

// GetMediaItemByFolderPath 按文件夹路径获取媒体条目（用于合并模式）
func GetMediaItemByFolderPath(folderPath string) (*model.MediaItem, error) {
	var item model.MediaItem
	result := db.Where("folder_path = ?", folderPath).First(&item)
	return &item, result.Error
}

// CreateOrUpdateMediaItem 创建或更新媒体条目（按 folder_path + file_name + album_name 组合唯一）
// 更新时保留已有的刮削数据，避免重新扫描时把已刮削的字段清空
//
// 注意：使用 Unscoped() 查询是为了把软删除残留记录也包含进来，
// 否则唯一索引 idx_media_folder_file_album 会与软删除行冲突，
// 导致新建条目时报 UNIQUE constraint failed。
func CreateOrUpdateMediaItem(item *model.MediaItem) error {
	var existing model.MediaItem
	result := db.Unscoped().Where("folder_path = ? AND file_name = ? AND album_name = ?", item.FolderPath, item.FileName, item.AlbumName).First(&existing)
	if result.Error == gorm.ErrRecordNotFound {
		return db.Create(item).Error
	}
	if result.Error != nil {
		return result.Error
	}
	item.ID = existing.ID
	item.CreatedAt = existing.CreatedAt
	// 复用已有记录时清除软删除标记，确保该条目“恢复”为正常记录
	item.DeletedAt = gorm.DeletedAt{}
	// 如果已有刮削数据，保留刮削字段，防止重新扫描时覆盖刮削结果
	if existing.ScrapedAt != nil {
		item.ScrapedAt = existing.ScrapedAt
		item.ScrapedName = existing.ScrapedName
		item.Cover = existing.Cover
		item.AlbumName = existing.AlbumName
		item.AlbumArtist = existing.AlbumArtist
		item.TrackNumber = existing.TrackNumber
		item.Duration = existing.Duration
		item.Genre = existing.Genre
		item.ReleaseDate = existing.ReleaseDate
		item.Rating = existing.Rating
		item.Plot = existing.Plot
		item.Authors = existing.Authors
		item.Description = existing.Description
		item.Publisher = existing.Publisher
		item.ISBN = existing.ISBN
		item.ExternalID = existing.ExternalID
	}
	return db.Unscoped().Save(item).Error
}

// UpdateMediaItem 更新媒体条目
// 使用 Unscoped 确保即使存在软删除标记也能正确更新（避免命中唯一索引但行不可见的诡异情况）。
func UpdateMediaItem(item *model.MediaItem) error {
	return db.Unscoped().Save(item).Error
}

// DeleteMediaItem 硬删除媒体条目（真正从数据库删除）
func DeleteMediaItem(id uint) error {
	return db.Unscoped().Delete(&model.MediaItem{}, id).Error
}

// ClearMediaItems 硬删除指定类型的所有媒体条目（真正从数据库删除）
func ClearMediaItems(mediaType model.MediaType) error {
	return db.Unscoped().Where("media_type = ?", mediaType).Delete(&model.MediaItem{}).Error
}

// ClearMediaItemsByScanPath 硬删除指定扫描路径的所有媒体条目
func ClearMediaItemsByScanPath(scanPathID uint) error {
	return db.Unscoped().Where("scan_path_id = ?", scanPathID).Delete(&model.MediaItem{}).Error
}

// ClearMediaScrapedData 清空指定类型的所有刮削数据（保留扫描出的文件记录本身）
// 仅清空刮削结果相关字段，不删除条目，便于重新刮削。
// mediaType 为空时表示对所有类型生效。
func ClearMediaScrapedData(mediaType model.MediaType) (int64, error) {
	tx := db.Unscoped().Model(&model.MediaItem{})
	if mediaType != "" {
		tx = tx.Where("media_type = ?", mediaType)
	}
	updates := map[string]interface{}{
		"scraped_name": "",
		"description":  "",
		"cover":        "",
		"release_date": "",
		"rating":       0,
		"genre":        "",
		"authors":      "",
		"plot":         "",
		"reviews":      "",
		"external_id":  "",
		"album_artist": "",
		"publisher":    "",
		"isbn":         "",
		"lyrics":       "",
		"scraped_at":   nil,
	}
	result := tx.Updates(updates)
	return result.RowsAffected, result.Error
}

// ListAllValidMediaItemsForCheck 列出指定类型下所有未删除的媒体条目（仅取必要字段，用于失效检查）
// mediaType 为空时返回所有类型条目。
func ListAllValidMediaItemsForCheck(mediaType model.MediaType) ([]model.MediaItem, error) {
	var items []model.MediaItem
	tx := db.Model(&model.MediaItem{}).
		Select("id, media_type, scan_path_id, file_name, folder_path, is_folder")
	if mediaType != "" {
		tx = tx.Where("media_type = ?", mediaType)
	}
	err := tx.Find(&items).Error
	return items, err
}

// DeleteMediaItemsByIDs 按ID列表硬删除媒体条目
func DeleteMediaItemsByIDs(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return db.Unscoped().Where("id IN ?", ids).Delete(&model.MediaItem{}).Error
}

// ListAlbums 列出所有专辑（音乐专用）
func ListAlbums(q MediaItemQuery) ([]AlbumInfo, int64, error) {
	type albumRow struct {
		AlbumName   string
		AlbumArtist string
		Cover       string
		ReleaseDate string
		TrackCount  int
		ScanPathID  uint
	}

	// 构建基础查询
	baseQuery := db.Model(&model.MediaItem{}).
		Where("media_type = ?", model.MediaTypeMusic)
	if q.Hidden != nil {
		baseQuery = baseQuery.Where("hidden = ?", *q.Hidden)
	}
	if q.ScanPathID > 0 {
		baseQuery = baseQuery.Where("scan_path_id = ?", q.ScanPathID)
	}
	if q.Keyword != "" {
		like := "%" + q.Keyword + "%"
		baseQuery = baseQuery.Where("album_name LIKE ? OR album_artist LIKE ? OR scraped_name LIKE ?", like, like, like)
	}

	// 统计分组数（用子查询）
	var total int64
	if err := db.Table("(?) as sub", baseQuery.
		Select("album_name, album_artist").
		Group("album_name, album_artist")).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if q.PageSize <= 0 {
		q.PageSize = 20
	}
	if q.Page <= 0 {
		q.Page = 1
	}

	tx := baseQuery.
		Select("album_name, album_artist, MAX(cover) as cover, MAX(release_date) as release_date, COUNT(*) as track_count, MAX(scan_path_id) as scan_path_id").
		Group("album_name, album_artist").
		Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize)

	var rows []albumRow
	if err := tx.Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	albums := make([]AlbumInfo, len(rows))
	for i, r := range rows {
		albums[i] = AlbumInfo{
			AlbumName:   r.AlbumName,
			AlbumArtist: r.AlbumArtist,
			Cover:       r.Cover,
			ReleaseDate: r.ReleaseDate,
			TrackCount:  r.TrackCount,
			ScanPathID:  r.ScanPathID,
		}
	}
	return albums, total, nil
}

// AlbumInfo 专辑信息
type AlbumInfo struct {
	AlbumName   string `json:"album_name"`
	AlbumArtist string `json:"album_artist"`
	Cover       string `json:"cover"`
	ReleaseDate string `json:"release_date"`
	TrackCount  int    `json:"track_count"`
	ScanPathID  uint   `json:"scan_path_id"`
}

// GetAlbumTracks 获取专辑曲目列表
// 支持两种模式：
//  1. 普通模式（is_folder=false）：直接返回独立文件记录
//  2. 合并文件夹模式（is_folder=true）：把 episodes 展开成虚拟 MediaItem 列表
//     展开后每条记录的 folder_path = 原folder_path/file_name（文件夹实际路径），file_name = episode.FileName
func GetAlbumTracks(albumName, albumArtist string) ([]model.MediaItem, error) {
	var items []model.MediaItem
	tx := db.Where("media_type = ?", model.MediaTypeMusic)
	if albumName != "" {
		tx = tx.Where("album_name = ?", albumName)
	} else {
		tx = tx.Where("(album_name = '' OR album_name IS NULL)")
	}
	if albumArtist != "" {
		tx = tx.Where("album_artist = ?", albumArtist)
	}
	err := tx.Order("track_number asc").Find(&items).Error
	if err != nil {
		return nil, err
	}

	// 展开合并文件夹条目的 episodes
	var result []model.MediaItem
	for _, item := range items {
		if !item.IsFolder || item.Episodes == "" {
			result = append(result, item)
			continue
		}
		// 解析 episodes
		type EpisodeInfo struct {
			FileName string `json:"file_name"`
			Index    int    `json:"index"`
			Title    string `json:"title"`
		}
		var eps []EpisodeInfo
		if err := json.Unmarshal([]byte(item.Episodes), &eps); err != nil || len(eps) == 0 {
			// 解析失败则跳过该条目（不返回文件夹本身，避免播放路径错误）
			continue
		}
		// 文件夹实际路径 = folder_path + "/" + file_name
		actualDir := strings.TrimRight(item.FolderPath, "/") + "/" + item.FileName
		for _, ep := range eps {
			track := item // 复制基础信息（封面、专辑名、艺术家等）
			track.ID = 0
			track.IsFolder = false
			track.FolderPath = actualDir
			track.FileName = ep.FileName
			track.TrackNumber = ep.Index
			track.ScrapedName = ep.Title
			track.Episodes = ""
			result = append(result, track)
		}
	}
	return result, nil
}

// ListFolderPaths 列出指定媒体类型下的所有文件夹路径（目录浏览模式）
func ListFolderPaths(mediaType model.MediaType) ([]string, error) {
	var paths []string
	err := db.Model(&model.MediaItem{}).
		Where("media_type = ?", mediaType).
		Distinct("folder_path").
		Pluck("folder_path", &paths).Error
	return paths, err
}

// GetUnscrappedItems 获取未刮削或刮削不完整的媒体条目
// 只要 scraped_at 为空，或 cover/scraped_name/description 任一为空，就需要重新刮削
// limit <= 0 表示不限制数量，返回所有未刮削条目
func GetUnscrappedItems(mediaType model.MediaType, limit int) ([]model.MediaItem, error) {
	var items []model.MediaItem
	tx := db.Where(
		"media_type = ? AND (scraped_at IS NULL OR cover = '' OR cover IS NULL OR scraped_name = '' OR scraped_name IS NULL OR description = '' OR description IS NULL)",
		mediaType,
	)
	if limit > 0 {
		tx = tx.Limit(limit)
	}
	err := tx.Find(&items).Error
	return items, err
}

// ==================== 导入/导出 ====================

// ListAllMediaItems 列出指定媒体类型下的所有条目（不分页，用于导出）
// mediaType 为空时返回全部类型的所有条目
func ListAllMediaItems(mediaType model.MediaType) ([]model.MediaItem, error) {
	var items []model.MediaItem
	tx := db.Model(&model.MediaItem{})
	if mediaType != "" {
		tx = tx.Where("media_type = ?", mediaType)
	}
	err := tx.Order("id asc").Find(&items).Error
	return items, err
}

// ListMediaItemsByScanPath 列出指定扫描路径下的所有条目（不分页，用于导出）
func ListMediaItemsByScanPath(scanPathID uint) ([]model.MediaItem, error) {
	var items []model.MediaItem
	err := db.Where("scan_path_id = ?", scanPathID).Order("id asc").Find(&items).Error
	return items, err
}

// ListAllMediaScanPaths 列出所有扫描路径（不区分类型，用于全量导出）
func ListAllMediaScanPaths() ([]model.MediaScanPath, error) {
	var paths []model.MediaScanPath
	err := db.Order("id asc").Find(&paths).Error
	return paths, err
}

// ImportMediaItems 批量导入媒体条目
// 策略：按 (folder_path, file_name, album_name) 唯一键 upsert：
//   - 已存在则覆盖刮削字段（导入是用户主动行为，覆盖优先于保留）
//   - 不存在则新建
//
// 注意：导入数据中的 ID/CreatedAt/UpdatedAt 会被忽略，由数据库重新分配，
// 避免和现有记录的主键冲突。
func ImportMediaItems(items []model.MediaItem, scanPathIDOverride *uint) (created, updated int, err error) {
	for i := range items {
		it := items[i]
		// 清空主键和时间戳，让数据库重新生成
		it.ID = 0
		it.CreatedAt = time.Time{}
		it.UpdatedAt = time.Time{}
		it.DeletedAt = gorm.DeletedAt{}
		// 如果指定了覆盖的 scan_path_id（按扫描路径导入场景），则强制覆盖
		if scanPathIDOverride != nil {
			it.ScanPathID = *scanPathIDOverride
		}

		var existing model.MediaItem
		result := db.Unscoped().Where(
			"folder_path = ? AND file_name = ? AND album_name = ?",
			it.FolderPath, it.FileName, it.AlbumName,
		).First(&existing)
		if result.Error == gorm.ErrRecordNotFound {
			if e := db.Create(&it).Error; e != nil {
				return created, updated, e
			}
			created++
			continue
		}
		if result.Error != nil {
			return created, updated, result.Error
		}
		// 覆盖更新
		it.ID = existing.ID
		it.CreatedAt = existing.CreatedAt
		it.DeletedAt = gorm.DeletedAt{}
		if e := db.Unscoped().Save(&it).Error; e != nil {
			return created, updated, e
		}
		updated++
	}
	return created, updated, nil
}

// ImportMediaScanPaths 批量导入扫描路径
// 策略：按 (media_type, path) 唯一对去重：
//   - 已存在则更新名称/标签等可编辑字段
//   - 不存在则新建并返回新ID
//
// 返回值 idMap：导入数据中的原始ID -> 数据库实际ID 的映射，
// 供随后的 MediaItem 导入用于把 scan_path_id 重新指向新ID。
func ImportMediaScanPaths(paths []model.MediaScanPath) (idMap map[uint]uint, created, updated int, err error) {
	idMap = make(map[uint]uint, len(paths))
	for i := range paths {
		p := paths[i]
		originalID := p.ID
		p.ID = 0
		p.CreatedAt = time.Time{}
		p.UpdatedAt = time.Time{}
		p.DeletedAt = gorm.DeletedAt{}

		var existing model.MediaScanPath
		result := db.Where("media_type = ? AND path = ?", p.MediaType, p.Path).First(&existing)
		if result.Error == gorm.ErrRecordNotFound {
			if e := db.Create(&p).Error; e != nil {
				return idMap, created, updated, e
			}
			idMap[originalID] = p.ID
			created++
			continue
		}
		if result.Error != nil {
			return idMap, created, updated, result.Error
		}
		// 已存在：更新可编辑字段
		existing.Name = p.Name
		existing.PathMerge = p.PathMerge
		existing.TypeTag = p.TypeTag
		existing.ContentTags = p.ContentTags
		existing.EnableScrape = p.EnableScrape
		if e := db.Save(&existing).Error; e != nil {
			return idMap, created, updated, e
		}
		idMap[originalID] = existing.ID
		updated++
	}
	return idMap, created, updated, nil
}
