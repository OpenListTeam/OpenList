package model

import (
	"time"

	"gorm.io/gorm"
)

// MediaType 媒体类型，使用字符串便于后期扩展
type MediaType string

const (
	MediaTypeVideo MediaType = "video"
	MediaTypeMusic MediaType = "music"
	MediaTypeImage MediaType = "image"
	MediaTypeBook  MediaType = "book"
)

// MediaScanPath 媒体库扫描路径（每个媒体库可配置多个扫描路径）
type MediaScanPath struct {
	ID        uint           `gorm:"primarykey"           json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index"                json:"-"`

	MediaType    MediaType  `gorm:"size:32;index;not null" json:"media_type"`  // 所属媒体类型
	Name         string     `json:"name"`                                      // 路径名称（用于前端筛选显示）
	Path         string     `gorm:"not null"             json:"path"`          // VFS 扫描路径
	PathMerge    bool       `gorm:"default:false"        json:"path_merge"`    // 路径合并模式（子文件夹作为一个条目）
	TypeTag      string     `json:"type_tag"`                                  // 类型标签：电影、电视剧等
	ContentTags  string     `json:"content_tags"`                              // 内容标签：喜剧、惊悚等（逗号分隔）
	EnableScrape bool       `gorm:"default:true"         json:"enable_scrape"` // 是否启用刮削
	LastScanAt   *time.Time `json:"last_scan_at"`                              // 最后扫描时间
}

// MediaIndexColumnMax 复合唯一索引列（folder_path / file_name / album_name）的最大字符数
const MediaIndexColumnMax = 191

// MediaItem 媒体条目（统一表，通过 media_type 区分类型，便于后期扩展新类型）
type MediaItem struct {
	// 显式声明基础字段，避免同时嵌入 gorm.Model 后重复生成 deleted_at 索引列；
	// JSON 标签继续保持媒体 API 的字段命名约定。
	ID        uint           `gorm:"primarykey"           json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index"                json:"-"`
	// 基础信息
	MediaType  MediaType `gorm:"size:32;index;not null"  json:"media_type"`
	ScanPathID uint      `gorm:"index"                   json:"scan_path_id"` // 关联的扫描路径ID
	// 三个字段共同组成复合唯一索引。MySQL 对「有索引且未指定长度」的 string 会生成 longtext，
	// 而 TEXT 列不能直接建索引（AutoMigrate 会失败）；因此必须显式限定 size。
	// utf8mb4 下 InnoDB 索引总长上限 3072 字节：191*3*4 = 2292，留足余量。
	FileName string `gorm:"size:191;uniqueIndex:idx_media_folder_file_album" json:"file_name"`
	FileSize int64  `json:"file_size"`
	MimeType string `json:"mime_type"`
	Hidden   bool   `gorm:"default:false"           json:"hidden"`

	// 刮削/编辑信息
	ScrapedName string  `json:"scraped_name"`
	Description string  `gorm:"type:text"               json:"description"`
	Cover       string  `json:"cover"`                                  // 封面URL或本地路径
	ReleaseDate string  `json:"release_date"`                           // 发布时间，格式 YYYY-MM-DD
	Rating      float32 `json:"rating"`                                 // 评分 0-10
	Genre       string  `json:"genre"`                                  // 类型，逗号分隔，如 "动作,科幻"
	Authors     string  `gorm:"type:text"               json:"authors"` // 作者/演员，JSON数组字符串
	Plot        string  `gorm:"type:text"               json:"plot"`    // 剧情/内容介绍
	Reviews     string  `gorm:"type:text"               json:"reviews"` // 用户评价，JSON数组字符串

	// 外部ID（用于刮削关联）
	ExternalID string `json:"external_id"` // TMDB ID / Discogs ID / 豆瓣ID

	// 音乐专属字段
	AlbumName   string `gorm:"size:191;uniqueIndex:idx_media_folder_file_album" json:"album_name"` // 所属专辑名
	AlbumArtist string `json:"album_artist"`                                                       // 专辑艺术家
	TrackNumber int    `json:"track_number"`                                                       // 曲目编号
	Duration    int    `json:"duration"`                                                           // 时长（秒）
	Lyrics      string `gorm:"type:text"    json:"lyrics"`                                         // LRC格式歌词

	// 视频专属字段
	VideoType string `json:"video_type"` // "movie" 或 "tv"
	Season    int    `json:"season"`     // 季（电视剧）
	Episode   int    `json:"episode"`    // 集（电视剧）

	// 书籍专属字段
	Publisher string `json:"publisher"` // 出版社
	ISBN      string `json:"isbn"`      // ISBN

	// 目录合并模式
	IsFolder   bool   `gorm:"default:false"                                    json:"is_folder"`         // 是否为文件夹模式条目
	FolderPath string `gorm:"size:191;index;uniqueIndex:idx_media_folder_file_album" json:"folder_path"` // 扫描根路径（恒定为扫描路径，与file_name+album_name组合唯一）
	Episodes   string `gorm:"type:text"                                         json:"episodes"`         // 选集信息，JSON数组，格式：[{file_name,index,title},...]

	ScrapedAt *time.Time `json:"scraped_at"`
}

// MediaConfig 媒体库配置（每种类型一条记录）
type MediaConfig struct {
	ID        uint           `gorm:"primarykey"           json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index"                json:"-"`
	// 媒体类型只允许使用 MediaType 定义的短枚举值；显式长度使 MySQL 生成
	// varchar 而不是无法建立索引的 longtext，满足该字段的唯一约束。
	MediaType    MediaType  `gorm:"size:32;uniqueIndex;not null" json:"media_type"`
	Enabled      bool       `gorm:"default:false"        json:"enabled"`
	LastScanAt   *time.Time `json:"last_scan_at"`
	LastScrapeAt *time.Time `json:"last_scrape_at"`
	// ScraperConfig 该媒体类型独立的刮削配置（JSON）：
	// store_thumbnail / thumbnail_mode / thumbnail_path，未设置时回落到全局设置
	ScraperConfig string `gorm:"type:text" json:"scraper_config"`
}

// EpisodeInfo 选集信息（文件夹型媒体的单集描述）
type EpisodeInfo struct {
	FileName string `json:"file_name"` // 原始文件名（含扩展名）
	Index    int    `json:"index"`     // 序号，默认0，文件名开头有数字则取该数字
	Title    string `json:"title"`     // 选集标题（去掉序号后的文件名，不含扩展名）
}

// MediaScanProgress 扫描进度（内存中维护，不持久化）
type MediaScanProgress struct {
	MediaType  MediaType `json:"media_type"`
	ScanPathID uint      `json:"scan_path_id,omitempty"` // 0 表示全部路径
	Running    bool      `json:"running"`
	Total      int       `json:"total"`
	Done       int       `json:"done"`
	Message    string    `json:"message"`
	Error      string    `json:"error,omitempty"`
}
