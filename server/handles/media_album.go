package handles

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
)

// ListMediaAlbumsAdmin 后台按专辑聚合列出音乐条目
//
//	GET /api/admin/media/albums?page=&page_size=&scan_path_id=&keyword=
//
// 与公开的 /fs/media/albums 相比多了后台筛选能力（扫描路径 / 关键词），
// 但都走同一个 db.ListAlbums 聚合逻辑（album_name + album_artist 分组）。
func ListMediaAlbumsAdmin(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	page, pageSize = clampMediaPage(page, pageSize, 20, 500)

	var scanPathID uint
	if s := c.Query("scan_path_id"); s != "" {
		if id, err := strconv.ParseUint(s, 10, 64); err == nil {
			scanPathID = uint(id)
		}
	}

	albums, total, err := db.ListAlbums(db.MediaItemQuery{
		MediaType:  model.MediaTypeMusic,
		ScanPathID: scanPathID,
		Keyword:    c.Query("keyword"),
		Page:       page,
		PageSize:   pageSize,
	})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, common.PageResp{Content: albums, Total: total})
}

// ListMediaAlbumItemsAdmin 列出某张专辑下的真实条目（后台编辑用）
//
//	GET /api/admin/media/album/items?album_name=&album_artist=&scan_path_id=
func ListMediaAlbumItemsAdmin(c *gin.Context) {
	var scanPathID uint
	if s := c.Query("scan_path_id"); s != "" {
		if id, err := strconv.ParseUint(s, 10, 64); err == nil {
			scanPathID = uint(id)
		}
	}
	items, err := db.ListMediaItemsByAlbum(
		c.Query("album_name"),
		c.Query("album_artist"),
		scanPathID,
	)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, items)
}

// UpdateMediaAlbumReq 专辑级批量修改
//
// 专辑是「按 album_name + album_artist 聚合出来的虚拟分组」，没有独立表，
// 因此这里把改动一次性写到该专辑下的所有条目（封面 / 简介 / 年份 / 类型 / 名称）。
type UpdateMediaAlbumReq struct {
	// 定位旧专辑
	AlbumName   *string `json:"album_name"`
	AlbumArtist *string `json:"album_artist"`
	ScanPathID  uint    `json:"scan_path_id"`
	// 新值
	NewAlbumName   *string `json:"new_album_name"`
	NewAlbumArtist *string `json:"new_album_artist"`
	Cover          *string `json:"cover"`
	ReleaseDate    *string `json:"release_date"`
	Genre          *string `json:"genre"`
	Plot           *string `json:"plot"`
	Description    *string `json:"description"`
}

// UpdateMediaAlbumAdmin 按专辑批量更新
//
//	POST /api/admin/media/album/update
func UpdateMediaAlbumAdmin(c *gin.Context) {
	var req UpdateMediaAlbumReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.AlbumName == nil && req.AlbumArtist == nil {
		common.ErrorStrResp(c, "缺少专辑定位字段", 400)
		return
	}
	albumName, albumArtist := "", ""
	if req.AlbumName != nil {
		albumName = *req.AlbumName
	}
	if req.AlbumArtist != nil {
		albumArtist = *req.AlbumArtist
	}
	items, err := db.ListMediaItemsByAlbum(albumName, albumArtist, req.ScanPathID)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if len(items) == 0 {
		common.ErrorStrResp(c, "专辑下没有曲目", 404)
		return
	}

	if req.Cover != nil && len(*req.Cover) > maxCoverFieldLen {
		common.ErrorStrResp(c, "封面数据过大（上限 12MB）", 400)
		return
	}

	// 先只改内存，最后统一落库：整批成功才动旧封面文件，
	// 避免「删了旧图但事务回滚」导致封面指向不存在的文件
	oldCovers := make([]string, 0, len(items))
	for i := range items {
		item := &items[i]
		if req.NewAlbumName != nil {
			item.AlbumName = *req.NewAlbumName
			// 合并文件夹型条目：显示名也跟着走，否则列表里还是旧专辑名
			if item.IsFolder && item.ScrapedName != "" {
				item.ScrapedName = *req.NewAlbumName
			}
		}
		if req.NewAlbumArtist != nil {
			item.AlbumArtist = *req.NewAlbumArtist
		}
		if req.Cover != nil {
			if item.Cover != *req.Cover {
				oldCovers = append(oldCovers, item.Cover)
			}
			item.Cover = *req.Cover
		}
		if req.ReleaseDate != nil {
			item.ReleaseDate = *req.ReleaseDate
		}
		if req.Genre != nil {
			item.Genre = *req.Genre
		}
		if req.Plot != nil {
			item.Plot = *req.Plot
		}
		if req.Description != nil {
			item.Description = *req.Description
		}
	}
	if err := db.UpdateMediaItems(items); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	for _, old := range oldCovers {
		removeUploadedCover(old, "")
	}
	common.SuccessResp(c, gin.H{"affected": len(items)})
}
