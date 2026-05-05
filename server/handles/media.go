package handles

import (
	"context"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/media"
	"github.com/OpenListTeam/OpenList/v4/internal/media/scraper"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// ==================== 配置管理 ====================

// ListMediaConfigs 获取所有媒体库配置
func ListMediaConfigs(c *gin.Context) {
	types := []model.MediaType{
		model.MediaTypeVideo,
		model.MediaTypeMusic,
		model.MediaTypeImage,
		model.MediaTypeBook,
	}
	cfgs := make([]*model.MediaConfig, 0, len(types))
	for _, t := range types {
		cfg, err := db.GetMediaConfig(t)
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
		cfgs = append(cfgs, cfg)
	}
	common.SuccessResp(c, cfgs)
}

// SaveMediaConfigReq 保存配置请求
type SaveMediaConfigReq struct {
	MediaType model.MediaType `json:"media_type" binding:"required"`
	Enabled   bool            `json:"enabled"`
}

// SaveMediaConfig 保存媒体库配置
func SaveMediaConfig(c *gin.Context) {
	var req SaveMediaConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	cfg := &model.MediaConfig{
		MediaType: req.MediaType,
		Enabled:   req.Enabled,
	}
	if err := db.SaveMediaConfig(cfg); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

// ==================== 扫描路径管理 ====================

// ListMediaScanPaths 获取指定媒体类型的扫描路径列表
func ListMediaScanPaths(c *gin.Context) {
	mediaType := model.MediaType(c.Query("media_type"))
	paths, err := db.ListMediaScanPaths(mediaType)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, paths)
}

// CreateMediaScanPathReq 创建扫描路径请求
type CreateMediaScanPathReq struct {
	MediaType    model.MediaType `json:"media_type" binding:"required"`
	Name         string          `json:"name"`
	Path         string          `json:"path" binding:"required"`
	PathMerge    bool            `json:"path_merge"`
	TypeTag      string          `json:"type_tag"`
	ContentTags  string          `json:"content_tags"`
	EnableScrape bool            `json:"enable_scrape"`
}

// CreateMediaScanPath 创建扫描路径
func CreateMediaScanPath(c *gin.Context) {
	var req CreateMediaScanPathReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.Path == "" {
		req.Path = "/"
	}
	name := req.Name
	if name == "" {
		name = req.Path
	}
	sp := &model.MediaScanPath{
		MediaType:    req.MediaType,
		Name:         name,
		Path:         req.Path,
		PathMerge:    req.PathMerge,
		TypeTag:      req.TypeTag,
		ContentTags:  req.ContentTags,
		EnableScrape: req.EnableScrape,
	}
	if err := db.CreateMediaScanPath(sp); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, sp)
}

// UpdateMediaScanPathReq 更新扫描路径请求
type UpdateMediaScanPathReq struct {
	ID           uint   `json:"id" binding:"required"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	PathMerge    bool   `json:"path_merge"`
	TypeTag      string `json:"type_tag"`
	ContentTags  string `json:"content_tags"`
	EnableScrape bool   `json:"enable_scrape"`
}

// UpdateMediaScanPath 更新扫描路径
func UpdateMediaScanPath(c *gin.Context) {
	var req UpdateMediaScanPathReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	sp, err := db.GetMediaScanPath(req.ID)
	if err != nil {
		common.ErrorResp(c, err, 404)
		return
	}
	sp.Name = req.Name
	sp.Path = req.Path
	sp.PathMerge = req.PathMerge
	sp.TypeTag = req.TypeTag
	sp.ContentTags = req.ContentTags
	sp.EnableScrape = req.EnableScrape
	if err := db.UpdateMediaScanPath(sp); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

// DeleteMediaScanPath 删除扫描路径
func DeleteMediaScanPath(c *gin.Context) {
	idStr := c.Query("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "无效的ID", 400)
		return
	}
	if err := db.DeleteMediaScanPath(uint(id)); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

// ClearMediaScanPathDB 清空指定扫描路径的媒体数据
func ClearMediaScanPathDB(c *gin.Context) {
	idStr := c.Query("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "无效的ID", 400)
		return
	}
	if err := db.ClearMediaItemsByScanPath(uint(id)); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

// ==================== 媒体条目管理（后台） ====================

// ListMediaItemsAdmin 后台分页查询媒体条目
func ListMediaItemsAdmin(c *gin.Context) {
	mediaType := model.MediaType(c.Query("media_type"))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	keyword := c.Query("keyword")
	orderBy := c.DefaultQuery("order_by", "name")
	orderDir := c.DefaultQuery("order_dir", "asc")
	scanPathIDStr := c.Query("scan_path_id")
	var scanPathID uint
	if scanPathIDStr != "" {
		if id, err := strconv.ParseUint(scanPathIDStr, 10, 64); err == nil {
			scanPathID = uint(id)
		}
	}

	q := db.MediaItemQuery{
		MediaType:  mediaType,
		ScanPathID: scanPathID,
		Keyword:    keyword,
		OrderBy:    orderBy,
		OrderDir:   orderDir,
		Page:       page,
		PageSize:   pageSize,
	}
	items, total, err := db.ListMediaItems(q)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, common.PageResp{Content: items, Total: total})
}

// UpdateMediaItemReq 更新媒体条目请求
type UpdateMediaItemReq struct {
	ID          uint    `json:"id" binding:"required"`
	ScrapedName string  `json:"scraped_name"`
	Description string  `json:"description"`
	Cover       string  `json:"cover"`
	ReleaseDate string  `json:"release_date"`
	Rating      float32 `json:"rating"`
	Genre       string  `json:"genre"`
	Authors     string  `json:"authors"`
	Plot        string  `json:"plot"`
	Reviews     string  `json:"reviews"`
	AlbumName   string  `json:"album_name"`
	AlbumArtist string  `json:"album_artist"`
	Publisher   string  `json:"publisher"`
	ISBN        string  `json:"isbn"`
	Hidden      bool    `json:"hidden"`
}

// UpdateMediaItemAdmin 后台更新媒体条目
func UpdateMediaItemAdmin(c *gin.Context) {
	var req UpdateMediaItemReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	item, err := db.GetMediaItemByID(req.ID)
	if err != nil {
		common.ErrorResp(c, err, 404)
		return
	}
	item.ScrapedName = req.ScrapedName
	item.Description = req.Description
	item.Cover = req.Cover
	item.ReleaseDate = req.ReleaseDate
	item.Rating = req.Rating
	item.Genre = req.Genre
	item.Authors = req.Authors
	item.Plot = req.Plot
	item.Reviews = req.Reviews
	item.AlbumName = req.AlbumName
	item.AlbumArtist = req.AlbumArtist
	item.Publisher = req.Publisher
	item.ISBN = req.ISBN
	item.Hidden = req.Hidden

	if err := db.UpdateMediaItem(item); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

// DeleteMediaItemAdmin 后台删除媒体条目
func DeleteMediaItemAdmin(c *gin.Context) {
	idStr := c.Query("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "无效的ID", 400)
		return
	}
	if err := db.DeleteMediaItem(uint(id)); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

// ClearMediaDB 清空指定类型媒体数据库
func ClearMediaDB(c *gin.Context) {
	mediaType := model.MediaType(c.Query("media_type"))
	if mediaType == "" {
		common.ErrorStrResp(c, "media_type 不能为空", 400)
		return
	}
	if err := db.ClearMediaItems(mediaType); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

// ==================== 扫描与刮削 ====================

// ScanMediaReq 扫描请求
type ScanMediaReq struct {
	MediaType  model.MediaType `json:"media_type" binding:"required"`
	ScanPathID uint            `json:"scan_path_id"` // 0 表示扫描全部路径
}

// StartMediaScan 开始扫描
func StartMediaScan(c *gin.Context) {
	var req ScanMediaReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	cfg, err := db.GetMediaConfig(req.MediaType)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if !cfg.Enabled {
		common.ErrorStrResp(c, "该媒体库未启用", 400)
		return
	}

	if req.ScanPathID > 0 {
		// 扫描单个路径
		sp, err := db.GetMediaScanPath(req.ScanPathID)
		if err != nil {
			common.ErrorResp(c, err, 404)
			return
		}
		media.ScanMediaPath(sp)
	} else {
		// 扫描全部路径
		media.ScanMedia(cfg)
	}
	common.SuccessResp(c)
}

// GetMediaScanProgress 获取扫描进度
func GetMediaScanProgress(c *gin.Context) {
	mediaType := model.MediaType(c.Query("media_type"))
	if mediaType == "" {
		common.ErrorStrResp(c, "media_type 不能为空", 400)
		return
	}
	progress := media.GetProgress(mediaType)
	common.SuccessResp(c, progress)
}

// ScrapeMediaReq 刮削请求
type ScrapeMediaReq struct {
	MediaType model.MediaType `json:"media_type" binding:"required"`
	ItemID    uint            `json:"item_id"` // 0 表示刮削全部未刮削的
}

// StartMediaScrape 开始刮削
func StartMediaScrape(c *gin.Context) {
	var req ScrapeMediaReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	cfg, err := db.GetMediaConfig(req.MediaType)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	// 从系统设置中读取刮削配置
	tmdbKey := setting.GetStr(conf.MediaTMDBKey)
	discogsToken := setting.GetStr(conf.MediaDiscogsToken)
	thumbnailMode := setting.GetStr(conf.MediaThumbnailMode, "base64")
	thumbnailPath := setting.GetStr(conf.MediaThumbnailPath, "/.thumbnail")
	storeThumbnail := setting.GetBool(conf.MediaStoreThumbnail)

	go func() {
		var items []model.MediaItem
		var err error

		if req.ItemID > 0 {
			item, e := db.GetMediaItemByID(req.ItemID)
			if e == nil {
				items = []model.MediaItem{*item}
			}
		} else {
			items, err = db.GetUnscrappedItems(req.MediaType, 100)
			if err != nil {
				log.Errorf("获取未刮削条目失败: %v", err)
				return
			}
		}

		for i := range items {
			item := &items[i]
			var scrapeErr error

			switch req.MediaType {
			case model.MediaTypeVideo:
				s := scraper.NewTMDBScraper(tmdbKey)
				scrapeErr = s.ScrapeVideo(item)
			case model.MediaTypeMusic:
				s := scraper.NewDiscogsScraper(discogsToken)
				scrapeErr = s.ScrapeMusic(item)
			case model.MediaTypeBook:
				doubanScraper := scraper.NewDoubanScraperWithConfig(
					thumbnailMode,
					thumbnailPath,
				)
				doubanErr := doubanScraper.ScrapeBook(item)
				if doubanErr != nil {
					log.Debugf("豆瓣刮削失败 [%s/%s]: %v，将尝试本地提取封面", item.FolderPath, item.FileName, doubanErr)
				}

				if item.Cover == "" {
					bookCtx, bookCancel := context.WithTimeout(context.Background(), 60*time.Second)
					// 书籍文件路径 = folder_path + "/" + file_name
					bookFilePath := item.FolderPath + "/" + item.FileName
					bookReader := media.FetchFileReader(bookCtx, bookFilePath)
					if bookReader != nil {
						localScraper := scraper.NewBookLocalScraperWithConfig(
							thumbnailMode,
							thumbnailPath,
						)
						if localCover := localScraper.ExtractLocalCover(item.FileName, bookFilePath, bookReader); localCover != "" {
							item.Cover = localCover
						}
						_ = bookReader.Close()
					}
					bookCancel()
				}

				if doubanErr != nil && item.Cover == "" {
					scrapeErr = doubanErr
				}
			case model.MediaTypeImage:
				imgCtx, imgCancel := context.WithTimeout(context.Background(), 30*time.Second)
				// 图片文件路径 = folder_path + "/" + file_name
				imgFilePath := item.FolderPath + "/" + item.FileName
				imgReader := media.FetchFileReader(imgCtx, imgFilePath)
				s := scraper.NewImageScraperWithConfig(
					storeThumbnail,
					thumbnailMode,
					thumbnailPath,
				)
				scrapeErr = s.ScrapeImage(item, imgReader)
				if imgReader != nil {
					_ = imgReader.Close()
				}
				imgCancel()
			}

			if scrapeErr != nil {
				log.Warnf("刮削失败 [%s] %s: %v", req.MediaType, item.FolderPath, scrapeErr)
				continue
			}
			now := time.Now()
			item.ScrapedAt = &now
			if err := db.UpdateMediaItem(item); err != nil {
				log.Warnf("保存刮削结果失败 [%s]: %v", item.FolderPath, err)
			}
		}
		log.Infof("刮削完成 [%s]，共处理 %d 条", req.MediaType, len(items))
	}()

	_ = cfg
	common.SuccessResp(c)
}

// ==================== 公开API（前端媒体库浏览） ====================

// PublicListMedia 公开媒体列表（前端浏览用）
func PublicListMedia(c *gin.Context) {
	mediaType := model.MediaType(c.Query("media_type"))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "40"))
	orderBy := c.DefaultQuery("order_by", "name")
	orderDir := c.DefaultQuery("order_dir", "asc")
	folderPath := c.Query("folder_path")
	keyword := c.Query("keyword")
	typeTag := c.Query("type_tag")
	contentTag := c.Query("content_tag")
	scanPathIDStr := c.Query("scan_path_id")
	var scanPathID uint
	if scanPathIDStr != "" {
		if id, err := strconv.ParseUint(scanPathIDStr, 10, 64); err == nil {
			scanPathID = uint(id)
		}
	}

	hidden := false
	q := db.MediaItemQuery{
		MediaType:  mediaType,
		ScanPathID: scanPathID,
		FolderPath: folderPath,
		TypeTag:    typeTag,
		ContentTag: contentTag,
		Hidden:     &hidden,
		Keyword:    keyword,
		OrderBy:    orderBy,
		OrderDir:   orderDir,
		Page:       page,
		PageSize:   pageSize,
	}
	items, total, err := db.ListMediaItems(q)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, common.PageResp{Content: items, Total: total})
}

// PublicGetMedia 公开获取媒体详情
func PublicGetMedia(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "无效的ID", 400)
		return
	}
	item, err := db.GetMediaItemByID(uint(id))
	if err != nil {
		common.ErrorResp(c, err, 404)
		return
	}
	if item.Hidden {
		common.ErrorStrResp(c, "资源不存在", 404)
		return
	}
	common.SuccessResp(c, item)
}

// PublicListAlbums 公开专辑列表（音乐）
func PublicListAlbums(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "40"))
	keyword := c.Query("keyword")
	scanPathIDStr := c.Query("scan_path_id")
	var scanPathID uint
	if scanPathIDStr != "" {
		if id, err := strconv.ParseUint(scanPathIDStr, 10, 64); err == nil {
			scanPathID = uint(id)
		}
	}

	hidden := false
	q := db.MediaItemQuery{
		MediaType:  model.MediaTypeMusic,
		ScanPathID: scanPathID,
		Hidden:     &hidden,
		Keyword:    keyword,
		Page:       page,
		PageSize:   pageSize,
	}
	albums, total, err := db.ListAlbums(q)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, common.PageResp{Content: albums, Total: total})
}

// PublicGetAlbum 公开获取专辑详情及曲目
func PublicGetAlbum(c *gin.Context) {
	albumName := c.Query("album_name")
	albumArtist := c.Query("album_artist")
	if albumName == "" && albumArtist == "" {
		common.ErrorStrResp(c, "album_name 和 album_artist 不能同时为空", 400)
		return
	}
	tracks, err := db.GetAlbumTracks(albumName, albumArtist)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, tracks)
}

// PublicListFolders 公开文件夹列表（目录浏览模式）
func PublicListFolders(c *gin.Context) {
	mediaType := model.MediaType(c.Query("media_type"))
	if mediaType == "" {
		common.ErrorStrResp(c, "media_type 不能为空", 400)
		return
	}
	paths, err := db.ListFolderPaths(mediaType)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, paths)
}

// PublicListScanPaths 公开获取扫描路径列表（前端筛选用）
func PublicListScanPaths(c *gin.Context) {
	mediaType := model.MediaType(c.Query("media_type"))
	paths, err := db.ListMediaScanPaths(mediaType)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, paths)
}
