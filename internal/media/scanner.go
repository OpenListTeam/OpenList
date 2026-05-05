package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	stdpath "path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

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

// ScanMedia 扫描媒体文件（异步），扫描指定媒体类型下的所有启用扫描路径
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

		// 获取该媒体类型下所有扫描路径
		scanPaths, err := db.ListMediaScanPaths(cfg.MediaType)
		if err != nil {
			p.mu.Lock()
			p.Error = err.Error()
			p.Message = "获取扫描路径失败"
			p.mu.Unlock()
			return
		}
		if len(scanPaths) == 0 {
			p.mu.Lock()
			p.Message = "没有配置扫描路径"
			p.mu.Unlock()
			return
		}

		for i := range scanPaths {
			sp := &scanPaths[i]
			p.mu.Lock()
			p.Message = "正在扫描路径: " + sp.Name
			p.mu.Unlock()

			if err := doScanPath(sp, p); err != nil {
				log.Errorf("media scan error [%s] path[%s]: %v", cfg.MediaType, sp.Path, err)
			} else {
				// 更新最后扫描时间
				now := time.Now()
				sp.LastScanAt = &now
				_ = db.UpdateMediaScanPath(sp)
			}
		}

		p.mu.Lock()
		p.Message = "扫描完成"
		p.mu.Unlock()
		// 更新媒体库最后扫描时间
		now := time.Now()
		cfg.LastScanAt = &now
		_ = db.SaveMediaConfig(cfg)
	}()
}

// ScanMediaPath 扫描单个扫描路径（异步）
func ScanMediaPath(sp *model.MediaScanPath) {
	p, ok := progressMap[sp.MediaType]
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
	p.Message = "正在扫描路径: " + sp.Name
	p.mu.Unlock()

	go func() {
		defer func() {
			p.mu.Lock()
			p.Running = false
			p.mu.Unlock()
		}()

		if err := doScanPath(sp, p); err != nil {
			p.mu.Lock()
			p.Error = err.Error()
			p.Message = "扫描失败"
			p.mu.Unlock()
			log.Errorf("media scan error [%s] path[%s]: %v", sp.MediaType, sp.Path, err)
		} else {
			p.mu.Lock()
			p.Message = "扫描完成"
			p.mu.Unlock()
			now := time.Now()
			sp.LastScanAt = &now
			_ = db.UpdateMediaScanPath(sp)
		}
	}()
}

func doScanPath(sp *model.MediaScanPath, p *ScanProgress) error {
	scanRoot := sp.Path
	if scanRoot == "" {
		scanRoot = "/"
	}

	// 收集所有待处理路径（VFS 虚拟路径）
	var targets []string

	ctx := context.Background()

	if sp.PathMerge {
		// 路径合并模式：
		//   - 子文件夹 → 作为一个合并条目（带 Episodes 选集信息）
		//   - 直接放在根目录下的单个媒体文件 → 正常作为独立条目扫描
		entries, err := fs.List(ctx, scanRoot, &fs.ListArgs{NoLog: true, Refresh: true})
		if err != nil {
			return err
		}
		for _, e := range entries {
			childPath := stdpath.Join(scanRoot, e.GetName())
			if e.IsDir() {
				targets = append(targets, childPath)
			} else if isMediaFile(e.GetName(), sp.MediaType) {
				targets = append(targets, childPath)
			}
		}
	} else {
		// 普通模式：递归扫描所有匹配文件（每个目录都刷新缓存）
		if err := walkVFS(ctx, scanRoot, sp.MediaType, &targets); err != nil {
			return err
		}
	}

	p.mu.Lock()
	p.Total += len(targets)
	p.mu.Unlock()

	// 音乐类型：按专辑合并处理
	if sp.MediaType == model.MediaTypeMusic {
		return doScanMusicByAlbum(ctx, targets, sp, p)
	}

	for _, target := range targets {
		item, err := buildMediaItemFromVFS(ctx, target, sp)
		if err != nil {
			log.Warnf("build media item error [%s]: %v", target, err)
			continue
		}

		// 路径合并模式下，扫描文件夹内的文件，填充选集信息
		if sp.PathMerge && item.IsFolder {
			if episodes, err := buildEpisodesFromFolder(ctx, target, sp.MediaType); err == nil {
				item.Episodes = episodes
			} else {
				log.Warnf("build episodes error [%s]: %v", target, err)
			}
		}

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

// doScanMusicByAlbum 音乐扫描
// 存储规则：
//   - 普通文件（is_folder=false）：
//     folder_path=文件所在目录，file_name=音乐文件名，episodes=空
//   - 合并文件夹（is_folder=true）：
//     folder_path=扫描根路径(sp.Path)，file_name=文件夹名，
//     episodes=文件夹内所有音乐文件列表，封面/标签取第一首
func doScanMusicByAlbum(ctx context.Context, targets []string, sp *model.MediaScanPath, p *ScanProgress) error {
	for _, target := range targets {
		obj, err := fs.Get(ctx, target, &fs.GetArgs{NoLog: true})
		if err != nil {
			log.Warnf("get vfs object error [%s]: %v", target, err)
			p.mu.Lock()
			p.Done++
			p.mu.Unlock()
			continue
		}

		name := obj.GetName()

		if obj.IsDir() {
			// ---- 合并文件夹模式：文件夹条目 ----
			// 列出文件夹内所有音乐文件
			entries, err := fs.List(ctx, target, &fs.ListArgs{NoLog: true})
			if err != nil {
				log.Warnf("list music folder error [%s]: %v", target, err)
				p.mu.Lock()
				p.Done++
				p.mu.Unlock()
				continue
			}

			var musicFiles []string
			var episodes []EpisodeInfo
			for idx, e := range entries {
				if e.IsDir() || !isMediaFile(e.GetName(), sp.MediaType) {
					continue
				}
				musicFiles = append(musicFiles, stdpath.Join(target, e.GetName()))
				ep := EpisodeInfo{
					FileName: e.GetName(),
					Index:    idx + 1,
					Title:    strings.TrimSuffix(e.GetName(), stdpath.Ext(e.GetName())),
				}
				episodes = append(episodes, ep)
			}

			if len(musicFiles) == 0 {
				p.mu.Lock()
				p.Done++
				p.mu.Unlock()
				continue
			}

			// 取第一首音乐文件读取标签
			firstFile := musicFiles[0]
			firstExt := strings.ToLower(stdpath.Ext(firstFile))
			var tag *MusicTag
			readCtx, readCancel := context.WithTimeout(ctx, 15*time.Second)
			if reader := FetchFileReader(readCtx, firstFile); reader != nil {
				switch firstExt {
				case ".flac":
					tag, _ = ParseFLACVorbisComment(reader)
				default:
					tag, _ = ParseID3v2(reader)
				}
				_ = reader.Close()
			}
			readCancel()

			// 解析标签（用文件夹名作为默认专辑名）
			albumName := name
			albumArtist := ""
			cover := ""
			releaseDate := ""
			genre := ""
			authors := ""
			if tag != nil {
				if tag.Album != "" {
					albumName = tag.Album
				}
				albumArtist = tag.AlbumArtist
				if albumArtist == "" {
					albumArtist = tag.Artist
				}
				if tag.Year != "" && len(tag.Year) >= 4 {
					releaseDate = tag.Year[:4] + "-01-01"
				}
				if tag.Genre != "" {
					genre = tag.Genre
				}
				if tag.Artist != "" {
					if authorsJSON, err := json.Marshal([]string{tag.Artist}); err == nil {
						authors = string(authorsJSON)
					}
				}
				if len(tag.CoverData) > 0 {
					mimeType := tag.CoverMIME
					if mimeType == "" {
						mimeType = "image/jpeg"
					}
					cover = "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(tag.CoverData)
				}
			}

			episodesJSON := ""
			if b, err := json.Marshal(episodes); err == nil {
				episodesJSON = string(b)
			}

			item := &model.MediaItem{
				MediaType:   sp.MediaType,
				ScanPathID:  sp.ID,
				FileName:    name,     // 文件夹名
				FolderPath:  sp.Path,  // 扫描根路径
				IsFolder:    true,
				AlbumName:   albumName,
				AlbumArtist: albumArtist,
				ScrapedName: albumName,
				Cover:       cover,
				ReleaseDate: releaseDate,
				Genre:       genre,
				Authors:     authors,
				Episodes:    episodesJSON,
			}

			if err := db.CreateOrUpdateMediaItem(item); err != nil {
				log.Warnf("save music folder item error [%s]: %v", target, err)
			}
		} else {
			// ---- 普通文件：每首歌独立一条记录 ----
			folderPath := stdpath.Dir(target)
			ext := strings.ToLower(stdpath.Ext(name))

			var tag *MusicTag
			readCtx, readCancel := context.WithTimeout(ctx, 15*time.Second)
			if reader := FetchFileReader(readCtx, target); reader != nil {
				switch ext {
				case ".flac":
					tag, _ = ParseFLACVorbisComment(reader)
				default:
					tag, _ = ParseID3v2(reader)
				}
				_ = reader.Close()
			}
			readCancel()

			albumName := ""
			albumArtist := ""
			trackTitle := strings.TrimSuffix(name, stdpath.Ext(name))
			trackNumber := 0
			cover := ""
			releaseDate := ""
			genre := ""
			authors := ""

			if tag != nil {
				albumName = tag.Album
				albumArtist = tag.AlbumArtist
				if albumArtist == "" {
					albumArtist = tag.Artist
				}
				if tag.Title != "" {
					trackTitle = tag.Title
				}
				trackNumber = tag.TrackNumber
				if tag.Year != "" && len(tag.Year) >= 4 {
					releaseDate = tag.Year[:4] + "-01-01"
				}
				if tag.Genre != "" {
					genre = tag.Genre
				}
				if tag.Artist != "" {
					if authorsJSON, err := json.Marshal([]string{tag.Artist}); err == nil {
						authors = string(authorsJSON)
					}
				}
				if len(tag.CoverData) > 0 {
					mimeType := tag.CoverMIME
					if mimeType == "" {
						mimeType = "image/jpeg"
					}
					cover = "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(tag.CoverData)
				}
			}

			item := &model.MediaItem{
				MediaType:   sp.MediaType,
				ScanPathID:  sp.ID,
				FileName:    name,
				FolderPath:  folderPath,
				IsFolder:    false,
				FileSize:    obj.GetSize(),
				MimeType:    mime.TypeByExtension(ext),
				AlbumName:   albumName,
				AlbumArtist: albumArtist,
				ScrapedName: trackTitle,
				Cover:       cover,
				ReleaseDate: releaseDate,
				Genre:       genre,
				Authors:     authors,
				TrackNumber: trackNumber,
			}

			if err := db.CreateOrUpdateMediaItem(item); err != nil {
				log.Warnf("save music item error [%s]: %v", target, err)
			}
		}

		p.mu.Lock()
		p.Done++
		p.Message = "已扫描: " + name
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

// walkVFS 递归遍历 VFS 路径，收集匹配的媒体文件路径（每个目录都刷新缓存）
func walkVFS(ctx context.Context, dirPath string, mediaType model.MediaType, targets *[]string) error {
	entries, err := fs.List(ctx, dirPath, &fs.ListArgs{NoLog: true, Refresh: true})
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

// buildMediaItemFromVFS 根据 VFS 路径构建 MediaItem（非音乐类型使用）
// 存储规则：
//   - folder_path：恒定为扫描根路径 sp.Path
//   - file_name：文件夹就是文件夹名，文件就是文件名
//   - episodes：文件夹时存里面每个文件的信息，文件时为空
func buildMediaItemFromVFS(ctx context.Context, vfsPath string, sp *model.MediaScanPath) (*model.MediaItem, error) {
	obj, err := fs.Get(ctx, vfsPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		return nil, err
	}

	name := obj.GetName()
	ext := strings.ToLower(stdpath.Ext(name))

	item := &model.MediaItem{
		MediaType:  sp.MediaType,
		ScanPathID: sp.ID,
		FileName:   name,                          // 文件夹名 或 文件名
		FolderPath: sp.Path,                       // 恒定为扫描根路径
		IsFolder:   obj.IsDir(),
		ScrapedName: strings.TrimSuffix(name, stdpath.Ext(name)), // 去掉扩展名作为默认名称
	}

	if !obj.IsDir() {
		// 普通文件：记录大小和 MIME 类型，episodes 为空
		item.FileSize = obj.GetSize()
		item.MimeType = mime.TypeByExtension(ext)
	}

	return item, nil
}

// episodeNumRe 匹配文件名开头的数字序号，支持 "1、" "2." "3-" "4 " 等分隔符
var episodeNumRe = regexp.MustCompile(`^(\d+)[、.\-\s_]+(.*)`)

// EpisodeInfo 选集信息
type EpisodeInfo struct {
	FileName string `json:"file_name"` // 原始文件名（含扩展名）
	Index    int    `json:"index"`     // 序号，默认0，文件名开头有数字则取该数字
	Title    string `json:"title"`     // 选集标题（去掉序号后的文件名，不含扩展名）
}

// buildEpisodesFromFolder 扫描文件夹内的媒体文件，构建选集信息 JSON 字符串
func buildEpisodesFromFolder(ctx context.Context, folderPath string, mediaType model.MediaType) (string, error) {
	entries, err := fs.List(ctx, folderPath, &fs.ListArgs{NoLog: true})
	if err != nil {
		return "", err
	}

	var episodes []EpisodeInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.GetName()
		if !isMediaFile(name, mediaType) {
			continue
		}

		// 去掉扩展名得到裸文件名
		ext := stdpath.Ext(name)
		baseName := strings.TrimSuffix(name, ext)

		ep := EpisodeInfo{
			FileName: name,
			Index:    0,
			Title:    baseName,
		}

		// 尝试从文件名开头提取数字序号
		if m := episodeNumRe.FindStringSubmatch(baseName); len(m) == 3 {
			if idx := parseLeadingInt(m[1]); idx > 0 {
				ep.Index = idx
				ep.Title = strings.TrimSpace(m[2])
			}
		} else {
			// 文件名直接以纯数字开头（无分隔符），也尝试提取
			if idx, rest := splitLeadingNumber(baseName); idx > 0 {
				ep.Index = idx
				ep.Title = strings.TrimSpace(rest)
			}
		}

		episodes = append(episodes, ep)
	}

	if len(episodes) == 0 {
		return "", nil
	}

	b, err := json.Marshal(episodes)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parseLeadingInt 将纯数字字符串解析为 int，失败返回 0
func parseLeadingInt(s string) int {
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		v = v*10 + int(c-'0')
	}
	return v
}

// splitLeadingNumber 从字符串开头提取连续数字，返回 (数字值, 剩余字符串)
// 仅当开头确实有数字时才返回非零值
func splitLeadingNumber(s string) (int, string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i == len(s) {
		// 没有数字，或者整个字符串都是数字（没有标题部分）
		return 0, s
	}
	// 剩余部分必须以非字母数字字符开头，避免把 "1080p" 之类的误识别
	if unicode.IsLetter(rune(s[i])) || unicode.IsDigit(rune(s[i])) {
		return 0, s
	}
	v := parseLeadingInt(s[:i])
	return v, s[i:]
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