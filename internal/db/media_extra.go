package db

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ==================== MediaAppKey ====================

func CreateMediaAppKey(k *model.MediaAppKey) error {
	return db.Create(k).Error
}

func ListMediaAppKeys(userID uint) ([]model.MediaAppKey, error) {
	var keys []model.MediaAppKey
	err := db.Where("user_id = ?", userID).Order("id asc").Find(&keys).Error
	return keys, err
}

func GetMediaAppKey(id uint) (*model.MediaAppKey, error) {
	var k model.MediaAppKey
	err := db.First(&k, id).Error
	return &k, err
}

func DeleteMediaAppKey(userID, id uint) error {
	return db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.MediaAppKey{}).Error
}

func TouchMediaAppKey(id uint) {
	now := time.Now()
	_ = db.Model(&model.MediaAppKey{}).Where("id = ?", id).Update("last_used_at", now).Error
}

// ListMediaAppKeysByPrefix 认证时按明文前缀预筛选候选密钥
func ListMediaAppKeysByPrefix(prefix string) ([]model.MediaAppKey, error) {
	var keys []model.MediaAppKey
	err := db.Where("prefix = ?", prefix).Find(&keys).Error
	return keys, err
}

// ==================== MediaUserData ====================

func GetMediaUserData(userID, itemID uint, episodeIndex int) (*model.MediaUserData, error) {
	var d model.MediaUserData
	err := db.Where("user_id = ? AND item_id = ? AND episode_index = ?", userID, itemID, episodeIndex).First(&d).Error
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func SaveMediaUserData(d *model.MediaUserData) error {
	var existing model.MediaUserData
	err := db.Where("user_id = ? AND item_id = ? AND episode_index = ?", d.UserID, d.ItemID, d.EpisodeIndex).First(&existing).Error
	if err == gorm.ErrRecordNotFound {
		return db.Create(d).Error
	}
	if err != nil {
		return err
	}
	d.ID = existing.ID
	d.CreatedAt = existing.CreatedAt
	return db.Save(d).Error
}

// ListMediaUserDataByItems 批量取用户对一组条目的播放状态
func ListMediaUserDataByItems(userID uint, itemIDs []uint) ([]model.MediaUserData, error) {
	var list []model.MediaUserData
	if len(itemIDs) == 0 {
		return list, nil
	}
	err := db.Where("user_id = ? AND item_id IN ?", userID, itemIDs).Find(&list).Error
	return list, err
}

// ListMediaFavorites 用户收藏的条目
func ListMediaFavorites(userID uint) ([]model.MediaUserData, error) {
	var list []model.MediaUserData
	err := db.Where("user_id = ? AND is_favorite = ?", userID, true).Order("updated_at desc").Find(&list).Error
	return list, err
}

// ListMediaResume 继续观看：未看完且有进度
func ListMediaResume(userID uint, limit int) ([]model.MediaUserData, error) {
	var list []model.MediaUserData
	if limit <= 0 {
		limit = 50
	}
	err := db.Where("user_id = ? AND played = ? AND position_ticks > 0", userID, false).
		Order("last_played_at desc").Limit(limit).Find(&list).Error
	return list, err
}

// ClearMediaUserDataByUser 删除用户全部播放状态
func ClearMediaUserDataByUser(userID uint) error {
	return db.Where("user_id = ?", userID).Delete(&model.MediaUserData{}).Error
}

// ==================== MediaPlaylist ====================

func CreateMediaPlaylist(p *model.MediaPlaylist) error {
	return db.Create(p).Error
}

func GetMediaPlaylist(id uint) (*model.MediaPlaylist, error) {
	var p model.MediaPlaylist
	err := db.First(&p, id).Error
	return &p, err
}

func ListMediaPlaylists(userID uint) ([]model.MediaPlaylist, error) {
	var list []model.MediaPlaylist
	err := db.Where("user_id = ?", userID).Order("id asc").Find(&list).Error
	return list, err
}

func UpdateMediaPlaylist(p *model.MediaPlaylist) error {
	return db.Save(p).Error
}

func DeleteMediaPlaylist(id uint) error {
	if err := db.Where("playlist_id = ?", id).Delete(&model.MediaPlaylistItem{}).Error; err != nil {
		return err
	}
	return db.Delete(&model.MediaPlaylist{}, id).Error
}

// ReplaceMediaPlaylistItems 覆盖式写入播放列表条目
func ReplaceMediaPlaylistItems(playlistID uint, items []model.MediaPlaylistItem) error {
	if err := db.Where("playlist_id = ?", playlistID).Delete(&model.MediaPlaylistItem{}).Error; err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	for i := range items {
		items[i].ID = 0
		items[i].PlaylistID = playlistID
		items[i].Order = i
	}
	return db.Create(&items).Error
}

func ListMediaPlaylistItems(playlistID uint) ([]model.MediaPlaylistItem, error) {
	var items []model.MediaPlaylistItem
	err := db.Where("playlist_id = ?", playlistID).Order("sort_order asc").Find(&items).Error
	return items, err
}

// CountMediaPlaylistItems 统计条目数
func CountMediaPlaylistItems(playlistID uint) (int64, error) {
	var n int64
	err := db.Model(&model.MediaPlaylistItem{}).Where("playlist_id = ?", playlistID).Count(&n).Error
	return n, err
}

// ==================== EmbyDevice ====================

func CreateEmbyDevice(d *model.EmbyDevice) error {
	return db.Create(d).Error
}

func GetEmbyDeviceByTokenHash(hash string) (*model.EmbyDevice, error) {
	var d model.EmbyDevice
	err := db.Where("token_hash = ?", hash).First(&d).Error
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func DeleteEmbyDevice(userID uint, deviceID string) error {
	tx := db.Where("user_id = ?", userID)
	if deviceID != "" {
		tx = tx.Where("device_id = ?", deviceID)
	}
	return tx.Delete(&model.EmbyDevice{}).Error
}

func TouchEmbyDevice(id uint) {
	_ = db.Model(&model.EmbyDevice{}).Where("id = ?", id).Update("last_seen_at", time.Now()).Error
}

// ==================== MediaAnnotation ====================

// ListMediaAnnotations 列出某用户对某个条目的全部批注
func ListMediaAnnotations(userID, itemID uint) ([]model.MediaAnnotation, error) {
	list := []model.MediaAnnotation{}
	err := db.Where("user_id = ? AND item_id = ?", userID, itemID).
		Order("id asc").Find(&list).Error
	return list, err
}

// SyncMediaAnnotations 增量保存批注：upserts 覆盖同一 remote_id 的记录，deletes 按 remote_id 删除。
// 放在一个事务里，避免多设备同时保存时出现半新半旧的状态。
func SyncMediaAnnotations(userID, itemID uint, upserts []model.MediaAnnotation, deletes []string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for i := range upserts {
			row := upserts[i]
			row.ID = 0
			row.UserID, row.ItemID = userID, itemID
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{
					{Name: "user_id"}, {Name: "item_id"}, {Name: "remote_id"},
				},
				DoUpdates: clause.AssignmentColumns([]string{"anchor", "updated_at"}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		if len(deletes) > 0 {
			if err := tx.Where("user_id = ? AND item_id = ? AND remote_id IN ?", userID, itemID, deletes).
				Delete(&model.MediaAnnotation{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
