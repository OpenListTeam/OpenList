package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	stdpath "path"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

// 支持的文件扩展名
var (
	videoExts = map[string]bool{
		".mp4": true, ".mkv": true, ".avi": true, ".mov": true,
		".wmv": true, ".flv": true, ".webm": true, ".m4v": true,
		".ts": true, ".rmvb": true, ".rm": true, ".3gp": true,
	}
	musicExts = map[string]bool{
		".mp3": true, ".flac": true, ".aac": true, ".ogg": true,
		".wav": true, ".wma": true, ".m4a": true, ".ape": true,
		".opus": true, ".aiff": true,
	}
	imageExts = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
		".webp": true, ".bmp": true, ".tiff": true, ".svg": true,
		".heic": true, ".avif": true,
	}
	bookExts = map[string]bool{
		".epub": true, ".pdf": true, ".mobi": true, ".azw3": true,
		".txt": true, ".djvu": true, ".cbz": true, ".cbr": true,
	}
)

// ScanProgress 扫描进度（全局，按媒体类型维护）
type ScanProgress struct {
	mu      sync.RWMutex
	Running bool
	Total   int
	Done    int
	Message string
	Error   string
}

var progressMap = map[model.MediaType]*ScanProgress{
	model.MediaTypeVideo: {},
	model.MediaTypeMusic: {},
	model.MediaTypeImage: {},
	model.MediaTypeBook:  {},
}

// GetProgress 获取扫描进度
func GetProgress(mediaType model.MediaType) model.MediaScanProgress {
	p, ok := progressMap[mediaType]
	if !ok {
		return model.MediaScanProgress{MediaType: mediaType}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return model.MediaScanProgress{
		MediaType: mediaType,
		Running:   p.Running,
		Total:     p.Total,
		Done:      p.Done,
		Message:   p.Message,
		Error:     p.Error,
	}
}

// ScanMedia 扫描媒体文件（异步）
func ScanMedia(cfg *model.MediaConfig) {
	p, ok := progressMap[cfg.MediaType]
	if !ok {
		return
	}
	p.mu.Lock()
	if p.Running {
		p.mu.Unlock()
		return
	}
	p.Running = true
	p.Total = 0
	p.Done = 0
	p.Error = ""
	p.Message = "正在扫描..."
	p.mu.Unlock()

	go func() {
		defer func() {
			p.mu.Lock()
			p.Running = false
			p.mu.Unlock()
		}()

		if err := doScan(cfg, p); err != nil {
			p.mu.Lock()
			p.Error = err.Error()
			p.Message = "扫描失败"
			p.mu.Unlock()
			log.Errorf("media scan error [%s]: %v", cfg.MediaType, err)
		} else {
			p.mu.Lock()
			p.Message = "扫描完成"
			p.mu.Unlock()
			// 更新最后扫描时间
			now := time.Now()
			cfg.LastScanAt = &now
			_ = db.SaveMediaConfig(cfg)
		}
	}()
}

func doScan(cfg *model.MediaConfig, p *ScanProgress) error {
	scanRoot := cfg.ScanPath
	if scanRoot == "" {
		scanRoot = "/"
	}

	// 收集所有待处理路径（VFS 虚拟路径）
	var targets []string

	ctx := context.Background()

	if cfg.PathMerge {
		// 路径合并模式：每个子文件夹作为一个条目
		entries, err := fs.List(ctx, scanRoot, &fs.ListArgs{NoLog: true})
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				targets = append(targets, stdpath.Join(scanRoot, e.GetName()))
			}
		}
	} else {
		// 普通模式：递归扫描所有匹配文件
		if err := walkVFS(ctx, scanRoot, cfg.MediaType, &targets); err != nil {
			return err
		}
	}

	p.mu.Lock()
	p.Total = len(targets)
	p.mu.Unlock()

	for _, target := range targets {
		item, err := buildMediaItemFromVFS(ctx, target, cfg)
		if err != nil {
			log.Warnf("build media item error [%s]: %v", target, err)
			continue
		}

		// 书籍类型：扫描阶段只记录基本信息，不读取文件内容，不刮削
		// 封面提取和豆瓣刮削在用户手动触发刮削时进行

		if err := db.CreateOrUpdateMediaItem(item); err != nil {
			log.Warnf("save media item error [%s]: %v", target, err)
		}
		p.mu.Lock()
		p.Done++
		p.Message = "已扫描: " + stdpath.Base(target)
		p.mu.Unlock()
	}
	return nil
}

// FetchFileReader 通过 VFS 路径获取文件内容流（用于刮削器读取文件内容）
// 优先使用 RangeReader 直接读取（本地存储无需 HTTP），失败时回退到 HTTP URL
// 返回 nil 表示无法获取（不影响主流程）
func FetchFileReader(ctx context.Context, vfsPath string) io.ReadCloser {
	link, _, err := fs.Link(ctx, vfsPath, model.LinkArgs{})
	if err != nil || link == nil {
		return nil
	}
	// 优先使用 RangeReader（本地存储直接读取，无需 HTTP 请求）
	if link.RangeReader != nil {
		rc, err := link.RangeReader.RangeRead(ctx, http_range.Range{Start: 0, Length: -1})
		if err == nil && rc != nil {
			return rc
		}
	}
	// 回退：通过 HTTP URL 读取（远程存储）
	if link.URL == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		return nil
	}
	return resp.Body
}

// walkVFS 递归遍历 VFS 路径，收集匹配的媒体文件路径
func walkVFS(ctx context.Context, dirPath string, mediaType model.MediaType, targets *[]string) error {
	entries, err := fs.List(ctx, dirPath, &fs.ListArgs{NoLog: true})
	if err != nil {
		log.Warnf("media scan: list vfs path [%s] error: %v", dirPath, err)
		return nil // 跳过无权限目录，不中断整体扫描
	}
	for _, e := range entries {
		childPath := stdpath.Join(dirPath, e.GetName())
		if e.IsDir() {
			if err := walkVFS(ctx, childPath, mediaType, targets); err != nil {
				return err
			}
		} else if isMediaFile(e.GetName(), mediaType) {
			*targets = append(*targets, childPath)
		}
	}
	return nil
}

// buildMediaItemFromVFS 根据 VFS 路径构建 MediaItem
func buildMediaItemFromVFS(ctx context.Context, vfsPath string, cfg *model.MediaConfig) (*model.MediaItem, error) {
	obj, err := fs.Get(ctx, vfsPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		return nil, err
	}

	name := obj.GetName()
	folderPath := stdpath.Dir(vfsPath)

	item := &model.MediaItem{
		MediaType:  cfg.MediaType,
		FilePath:   vfsPath,
		FileName:   name,
		FolderPath: folderPath,
		IsFolder:   obj.IsDir(),
	}

	if !obj.IsDir() {
		item.FileSize = obj.GetSize()
		ext := strings.ToLower(stdpath.Ext(name))
		item.MimeType = mime.TypeByExtension(ext)
	}

	// 路径合并模式：使用文件夹名作为名称
	if cfg.PathMerge && obj.IsDir() {
		item.ScrapedName = name
	} else {
		// 去掉扩展名作为默认名称
		ext := stdpath.Ext(name)
		item.ScrapedName = strings.TrimSuffix(name, ext)
	}

	// 音乐文件：尝试读取标签（MP3 读 ID3v2，FLAC 读 Vorbis Comment），填充专辑/艺术家/曲目等元数据
	if cfg.MediaType == model.MediaTypeMusic && !obj.IsDir() {
		ext := strings.ToLower(stdpath.Ext(name))
		readCtx, readCancel := context.WithTimeout(ctx, 15*time.Second)
		if reader := FetchFileReader(readCtx, vfsPath); reader != nil {
			var tag *MusicTag
			switch ext {
			case ".flac":
				tag, _ = ParseFLACVorbisComment(reader)
			default:
				// MP3 及其他格式尝试 ID3v2
				tag, _ = ParseID3v2(reader)
			}
			if tag != nil {
				if tag.Title != "" {
					item.ScrapedName = tag.Title
				}
				item.AlbumName = tag.Album
				item.AlbumArtist = tag.AlbumArtist
				if item.AlbumArtist == "" {
					item.AlbumArtist = tag.Artist
				}
				// 将艺术家写入 Authors 字段（JSON 数组格式）
				if tag.Artist != "" {
					if authorsJSON, err := json.Marshal([]string{tag.Artist}); err == nil {
						item.Authors = string(authorsJSON)
					}
				}
				item.TrackNumber = tag.TrackNumber
				if tag.Year != "" && len(tag.Year) >= 4 {
					item.ReleaseDate = tag.Year[:4] + "-01-01"
				}
				if tag.Genre != "" {
					item.Genre = tag.Genre
				}
				// 提取内嵌封面图片，转为 data URI 存入 Cover（仅当 Cover 为空时）
				if item.Cover == "" && len(tag.CoverData) > 0 {
					mime := tag.CoverMIME
					if mime == "" {
						mime = "image/jpeg"
					}
					item.Cover = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(tag.CoverData)
				}
			}
			_ = reader.Close()
		}
		readCancel()
	}

	return item, nil
}

// isMediaFile 判断文件名是否为指定媒体类型（按扩展名判断）
func isMediaFile(name string, mediaType model.MediaType) bool {
	ext := strings.ToLower(stdpath.Ext(name))
	switch mediaType {
	case model.MediaTypeVideo:
		return videoExts[ext]
	case model.MediaTypeMusic:
		return musicExts[ext]
	case model.MediaTypeImage:
		return imageExts[ext]
	case model.MediaTypeBook:
		return bookExts[ext]
	}
	return false
}

// GetSupportedExts 获取指定媒体类型支持的扩展名列表
func GetSupportedExts(mediaType model.MediaType) []string {
	var extMap map[string]bool
	switch mediaType {
	case model.MediaTypeVideo:
		extMap = videoExts
	case model.MediaTypeMusic:
		extMap = musicExts
	case model.MediaTypeImage:
		extMap = imageExts
	case model.MediaTypeBook:
		extMap = bookExts
	default:
		return nil
	}
	exts := make([]string, 0, len(extMap))
	for ext := range extMap {
		exts = append(exts, ext)
	}
	return exts
}