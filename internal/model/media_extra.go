package model

import "time"

// MediaAppKey 媒体应用密钥。
// 用于无法走交互式登录的客户端（Subsonic 的 token 认证、Emby 的 api_key、
// MoviePilot 对接等）。密钥本身以 AES-GCM 加密存储，数据库中不保留明文。
type MediaAppKey struct {
	ID        uint      `gorm:"primarykey"                  json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	UserID uint   `gorm:"index;not null"       json:"user_id"`
	Name   string `json:"name"`      // 备注名，例如 "Symfonium"、"MoviePilot"
	Prefix string `gorm:"index"                json:"prefix"`    // 明文前缀（便于识别/排查）
	Secret string `gorm:"not null"             json:"-"`         // AES-GCM 密文（base64）

	LastUsedAt *time.Time `json:"last_used_at"`
}

// MediaUserData 用户对媒体条目的播放状态，Emby 与 Subsonic 共用。
type MediaUserData struct {
	ID        uint      `gorm:"primarykey"                                  json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	UserID       uint `gorm:"uniqueIndex:idx_media_user_item_ep;not null"   json:"user_id"`
	ItemID       uint `gorm:"uniqueIndex:idx_media_user_item_ep;not null"   json:"item_id"`
	EpisodeIndex int  `gorm:"uniqueIndex:idx_media_user_item_ep;not null"   json:"episode_index"` // 0 表示条目本身，>0 表示选集序号

	Played        bool       `json:"played"`
	PlayCount     int        `json:"play_count"`
	IsFavorite    bool       `gorm:"index"                                  json:"is_favorite"`
	Rating        int        `json:"rating"` // 0-5，Subsonic 语义
	PositionTicks int64      `json:"position_ticks"` // 100ns tick，与 Emby 一致
	LastPlayedAt  *time.Time `json:"last_played_at"`
}

// MediaPlaylist 用户播放列表（Subsonic 需要，Emby 的 Playlist 复用同一份数据）
type MediaPlaylist struct {
	ID        uint      `gorm:"primarykey"                       json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	UserID    uint   `gorm:"index;not null"           json:"user_id"`
	Name      string `json:"name"`
	Comment   string `json:"comment"`
	Public    bool   `json:"public"`
	ItemCount int    `json:"item_count"`
}

// MediaPlaylistItem 播放列表条目（按 Order 排序）
type MediaPlaylistItem struct {
	ID         uint `gorm:"primarykey"                    json:"id"`
	PlaylistID uint `gorm:"index;not null"                json:"playlist_id"`
	ItemID     uint `gorm:"index;not null"                json:"item_id"`
	EpisodeIndex int `json:"episode_index"`
	Order      int  `gorm:"column:sort_order"             json:"order"`
}

// MediaAnnotation 用户在 PDF 阅读器里添加的批注。
//
// 只存查看器原生的批注对象（Anchor，JSON）：页码、rect/quadPoints、颜色、
// 文字、绘制点集都在里面，拆成独立列会出现两份可能不一致的数据。
// 重新打开时原样回灌给 PDF 引擎，因此这里不重复建模。
type MediaAnnotation struct {
	ID        uint      `gorm:"primarykey"                    json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	UserID uint `gorm:"uniqueIndex:idx_media_annotation_key;not null"       json:"-"`
	ItemID uint `gorm:"uniqueIndex:idx_media_annotation_key;not null;index" json:"item_id"`
	// RemoteID 是查看器分配的批注 id：多设备打开同一份 PDF 时靠它做去重与更新
	RemoteID string `gorm:"uniqueIndex:idx_media_annotation_key;size:128;not null" json:"remote_id"`
	Anchor   string `gorm:"type:text;not null"                                json:"anchor"`
}

// EmbyDevice Emby 客户端会话（由 AuthenticateByName 签发）
type EmbyDevice struct {
	ID        uint      `gorm:"primarykey"             json:"id"`
	CreatedAt time.Time `json:"created_at"`

	UserID     uint      `gorm:"index;not null"        json:"user_id"`
	DeviceID   string    `json:"device_id"`   // 客户端上报的 DeviceId
	DeviceName string    `json:"device_name"`
	Client     string    `json:"client"`
	Version    string    `json:"version"`
	TokenHash  string    `gorm:"uniqueIndex;not null"  json:"-"`
	AppKeyID   uint      `json:"app_key_id"` // 通过应用密钥认证时为对应 key，否则为 0
	LastSeenAt time.Time `json:"last_seen_at"`
}
