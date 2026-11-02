package handles

import (
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

const mediaTicksPerSecond = 10_000_000

// GetMediaUserDataBatch 批量查询当前用户对条目的播放状态（收藏/进度/已看）
// GET /fs/media/userdata?ids=1,2,3
func GetMediaUserDataBatch(c *gin.Context) {
	user := mediaUser(c)
	if user == nil {
		common.SuccessResp(c, []model.MediaUserData{})
		return
	}
	raw := c.Query("ids")
	ids := make([]uint, 0)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if id, err := strconv.ParseUint(part, 10, 64); err == nil {
			ids = append(ids, uint(id))
		}
	}
	if len(ids) == 0 {
		common.SuccessResp(c, []model.MediaUserData{})
		return
	}
	// 限制批量查询数量，避免超长 IN 子句造成数据库压力
	if len(ids) > 500 {
		ids = ids[:500]
	}
	list, err := db.ListMediaUserDataByItems(user.ID, ids)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, list)
}

// GetMediaProgress 查询单个条目（或选集）的播放进度
func GetMediaProgress(c *gin.Context) {
	user := mediaUser(c)
	itemID := uint(atoiDefault(c.Query("item_id"), 0))
	episode := atoiDefault(c.Query("episode_index"), 0)
	if user == nil || itemID == 0 {
		common.SuccessResp(c, nil)
		return
	}
	item, err := db.GetMediaItemByID(itemID)
	if err != nil || item.Hidden || !CanUserAccessItem(user, item) {
		common.SuccessResp(c, nil)
		return
	}
	data, err := db.GetMediaUserData(user.ID, itemID, episode)
	if err != nil {
		common.SuccessResp(c, nil)
		return
	}
	common.SuccessResp(c, data)
}

// SaveMediaProgress 上报播放进度
type saveProgressReq struct {
	ItemID       uint    `json:"item_id"`
	EpisodeIndex int     `json:"episode_index"`
	PositionSec  float64 `json:"position"`
	DurationSec  float64 `json:"duration"`
	Played       bool    `json:"played"`
}

func SaveMediaProgress(c *gin.Context) {
	user := mediaUser(c)
	if user == nil {
		common.ErrorStrResp(c, "unauthorized", 401)
		return
	}
	var req saveProgressReq
	if err := c.ShouldBindJSON(&req); err != nil || req.ItemID == 0 {
		common.ErrorStrResp(c, "invalid request", 400)
		return
	}
	item, err := db.GetMediaItemByID(req.ItemID)
	if err != nil || item.Hidden || !CanUserAccessItem(user, item) {
		common.ErrorStrResp(c, "not found", 404)
		return
	}
	data, err := db.GetMediaUserData(user.ID, req.ItemID, req.EpisodeIndex)
	if err != nil {
		data = &model.MediaUserData{
			UserID:       user.ID,
			ItemID:       req.ItemID,
			EpisodeIndex: req.EpisodeIndex,
		}
	}
	now := time.Now()
	data.PositionTicks = int64(req.PositionSec * mediaTicksPerSecond)
	if req.DurationSec > 0 {
		data.PositionTicks = int64(minFloat(req.PositionSec, req.DurationSec) * mediaTicksPerSecond)
	}
	data.LastPlayedAt = &now
	finished := req.Played
	if req.DurationSec > 0 && req.PositionSec/req.DurationSec >= 0.9 {
		finished = true
	}
	if finished && !data.Played {
		data.Played = true
		data.PlayCount++
	}
	// 重新播放时清空已看标记
	if !finished && req.PositionSec < 5 && data.Played {
		data.Played = false
	}
	if err := db.SaveMediaUserData(data); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, data)
}

// SetMediaFavorite 收藏 / 取消收藏
type favoriteReq struct {
	ItemID       uint `json:"item_id"`
	EpisodeIndex int  `json:"episode_index"`
	Favorite     bool `json:"favorite"`
}

func SetMediaFavorite(c *gin.Context) {
	user := mediaUser(c)
	if user == nil {
		common.ErrorStrResp(c, "unauthorized", 401)
		return
	}
	var req favoriteReq
	if err := c.ShouldBindJSON(&req); err != nil || req.ItemID == 0 {
		common.ErrorStrResp(c, "invalid request", 400)
		return
	}
	item, err := db.GetMediaItemByID(req.ItemID)
	if err != nil || item.Hidden || !CanUserAccessItem(user, item) {
		common.ErrorStrResp(c, "not found", 404)
		return
	}
	data, err := db.GetMediaUserData(user.ID, req.ItemID, req.EpisodeIndex)
	if err != nil {
		data = &model.MediaUserData{
			UserID:       user.ID,
			ItemID:       req.ItemID,
			EpisodeIndex: req.EpisodeIndex,
		}
	}
	data.IsFavorite = req.Favorite
	if err := db.SaveMediaUserData(data); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, gin.H{"favorite": data.IsFavorite})
}

// ListMediaFavoritesAPI 列出当前用户收藏的媒体条目
// GET /fs/media/favorites?media_type=video&page=1&page_size=20
func ListMediaFavoritesAPI(c *gin.Context) {
	user := mediaUser(c)
	mediaType := model.MediaType(c.Query("media_type"))
	page := atoiDefault(c.DefaultQuery("page", "1"), 1)
	pageSize := atoiDefault(c.DefaultQuery("page_size", "20"), 20)
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	items := []model.MediaItem{}
	if user != nil {
		data, err := db.ListMediaFavorites(user.ID)
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
		for i := range data {
			row := data[i]
			if row.EpisodeIndex != 0 {
				continue
			}
			item, err := db.GetMediaItemByID(row.ItemID)
			if err != nil || item.Hidden || item.DeletedAt.Valid {
				continue
			}
			if mediaType != "" && item.MediaType != mediaType {
				continue
			}
			if !CanUserAccessItem(user, item) {
				continue
			}
			items = append(items, *item)
		}
	}
	total := len(items)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	common.SuccessResp(c, common.PageResp{Content: items[start:end], Total: int64(total)})
}

// ListMediaResumeAPI 继续观看列表
func ListMediaResumeAPI(c *gin.Context) {
	user := mediaUser(c)
	mediaType := model.MediaType(c.Query("media_type"))
	limit := atoiDefault(c.DefaultQuery("limit", "20"), 20)
	items := []model.MediaItem{}
	if user != nil {
		data, err := db.ListMediaResume(user.ID, 200)
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
		for i := range data {
			row := data[i]
			if row.PositionTicks <= 0 {
				continue
			}
			item, err := db.GetMediaItemByID(row.ItemID)
			if err != nil || item.Hidden || item.DeletedAt.Valid {
				continue
			}
			if mediaType != "" && item.MediaType != mediaType {
				continue
			}
			if !CanUserAccessItem(user, item) {
				continue
			}
			items = append(items, *item)
			if len(items) >= limit {
				break
			}
		}
	}
	common.SuccessResp(c, items)
}

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
