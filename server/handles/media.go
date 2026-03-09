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
	// 确保四种类型都有配置返回
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
	ScanPath  string          `json:"scan_path"`
	PathMerge bool            `json:"path_merge"`
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
		ScanPath:  req.ScanPath,
		PathMerge: req.PathMerge,
	}
	if cfg.ScanPath == "" {
		cfg.ScanPath = "/"
	}
	if err := db.SaveMediaConfig(cfg); err != nil {
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

	q := db.MediaItemQuery{
		MediaType: mediaType,
		Keyword:   keyword,
		OrderBy:   orderBy,
		OrderDir:  orderDir,
		Page:      page,
		PageSize:  pageSize,
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
	MediaType model.MediaType `json:"media_type" binding:"required"`
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
	media.ScanMedia(cfg)
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
				// 步骤1：优先通过豆瓣刮削获取书名、评分、简介、封面
				doubanScraper := scraper.NewDoubanScraperWithConfig(
					thumbnailMode,
					thumbnailPath,
				)
				doubanErr := doubanScraper.ScrapeBook(item)
				if doubanErr != nil {
					log.Debugf("豆瓣刮削失败 [%s]: %v，将尝试本地提取封面", item.FilePath, doubanErr)
				}

				// 步骤2：若豆瓣未能获取到封面（cover 为空），则本地读取文件提取封面
				// 绝不将文件路径作为 cover
				if item.Cover == "" {
					bookCtx, bookCancel := context.WithTimeout(context.Background(), 60*time.Second)
					bookReader := media.FetchFileReader(bookCtx, item.FilePath)
					if bookReader != nil {
						localScraper := scraper.NewBookLocalScraperWithConfig(
							thumbnailMode,
							thumbnailPath,
						)
						if localCover := localScraper.ExtractLocalCover(item.FileName, item.FilePath, bookReader); localCover != "" {
							item.Cover = localCover
						}
						_ = bookReader.Close()
					}
					bookCancel()
				}

				// 豆瓣刮削失败且本地也无封面时，整体视为刮削失败
				if doubanErr != nil && item.Cover == "" {
					scrapeErr = doubanErr
				}
			case model.MediaTypeImage:
				// 读取图片文件流，用于 EXIF 解析和缩略图生成
				imgCtx, imgCancel := context.WithTimeout(context.Background(), 30*time.Second)
				imgReader := media.FetchFileReader(imgCtx, item.FilePath)
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
				log.Warnf("刮削失败 [%s] %s: %v", req.MediaType, item.FilePath, scrapeErr)
				continue
			}
			// 标记刮削完成时间，避免下次刮削重复处理
			now := time.Now()
			item.ScrapedAt = &now
			if err := db.UpdateMediaItem(item); err != nil {
				log.Warnf("保存刮削结果失败 [%s]: %v", item.FilePath, err)
			}
		}
		log.Infof("刮削完成 [%s]，共处理 %d 条", req.MediaType, len(items))
	}()

	_ = cfg // cfg 仅用于校验媒体库是否存在，刮削配置已从系统设置读取
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

	hidden := false
	q := db.MediaItemQuery{
		MediaType:  mediaType,
		FolderPath: folderPath,
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

	hidden := false
	q := db.MediaItemQuery{
		MediaType: model.MediaTypeMusic,
		Hidden:    &hidden,
		Keyword:   keyword,
		Page:      page,
		PageSize:  pageSize,
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
