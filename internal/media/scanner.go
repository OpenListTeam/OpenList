package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	stdpath "path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// SkipFileMetadata 表示「不从文件读取信息」开关已打开。
//
// 打开后扫描与刮削都不再读取媒体文件内容（音乐 ID3 / Vorbis、书籍内嵌封面、
// 图片 EXIF），只用文件名与目录名推断元数据。适合网盘风控敏感、
// 或扫描大库时不想为每个文件产生一次读取请求的场景。
func SkipFileMetadata() bool {
	return setting.GetBool(conf.MediaSkipFileMetadata)
}

// 支持的文件扩展名
var (
	videoExts = map[string]bool{
		".mp4": true, ".mkv": true, ".avi": true, ".mov": true,
		".wmv": true, ".flv": true, ".webm": true, ".m4v": true,
		".ts": true, ".rmvb": true, ".rm": true, ".3gp": true,
		// 蓝光 / DVD 原盘镜像：按单文件影片入库并参与刮削（浏览器内无法直接播放，
		// 需要客户端播放器或后续转码支持）
		".iso": true,
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

// extraMediaDirNames 附属目录（花絮、字幕、样片…）的名字。
// 合并模式扫描到这些目录时直接跳过：它们不是独立的影片/专辑，
// 单独入库只会在媒体库里制造一堆无意义的条目。
// 目录名比较前会归一化（小写、去掉空格/点/下划线/连字符）。
var extraMediaDirNames = map[string]bool{
	"花絮": true, "幕后": true, "特典": true, "特辑": true,
	"预告": true, "预告片": true, "片段": true, "删减片段": true,
	"采访": true, "字幕": true, "样片": true,
	"sample": true, "samples": true, "extra": true, "extras": true,
	"trailer": true, "trailers": true, "featurette": true, "featurettes": true,
	"behindthescenes": true, "deletedscenes": true,
	"interview": true, "interviews": true,
	"subs": true, "sub": true, "subtitle": true, "subtitles": true,
}

// isExtraMediaDir 判断目录名是否属于附属内容目录
func isExtraMediaDir(name string) bool {
	return extraMediaDirNames[normalizeDirName(name)]
}

// seasonDirRe 匹配「季」目录名：第一季 / 第2季 / Season 1 / S01 / S1，
// 允许后缀（「第一季.1080p」「Season 1 - 4K」），但不会匹配 S01E01 这类单集目录。
var seasonDirRe = regexp.MustCompile(`(?i)^(第\s*[0-9一二三四五六七八九十百零两]+\s*季|season[\s._-]*[0-9]{1,2}|s[0-9]{1,2})([^0-9a-z]|$)`)

// isSeasonDir 判断目录名是否是「季」目录
func isSeasonDir(name string) bool {
	return seasonDirRe.MatchString(strings.TrimSpace(name))
}

var (
	// 第N季 / 第十二季
	seasonCNRe = regexp.MustCompile(`^第\s*([0-9]+|[一二三四五六七八九十两]+)\s*季`)
	// Season 1 / season.02
	seasonENRe = regexp.MustCompile(`(?i)^season[\s._-]*([0-9]{1,2})`)
	// S01 / s1（后面不能再跟数字或字母，避免命中 S01E01）
	seasonSRe = regexp.MustCompile(`(?i)^s([0-9]{1,2})([^0-9a-z]|$)`)
)

// seasonNumberOf 从季目录名解析季号（第一季→1，Season 2→2，S03→3），无法解析返回 0
func seasonNumberOf(name string) int {
	n := strings.TrimSpace(name)
	if m := seasonCNRe.FindStringSubmatch(n); len(m) == 2 {
		if v := parseLeadingInt(m[1]); v > 0 {
			return v
		}
		return chineseNumber(m[1])
	}
	if m := seasonENRe.FindStringSubmatch(n); len(m) == 2 {
		return parseLeadingInt(m[1])
	}
	// 该正则带边界分组，匹配成功时有 3 个元素
	if m := seasonSRe.FindStringSubmatch(n); len(m) >= 2 {
		return parseLeadingInt(m[1])
	}
	return 0
}

// chineseNumber 解析常见中文数字（一~九十九），失败返回 0
func chineseNumber(s string) int {
	if s == "" {
		return 0
	}
	digits := map[rune]int{
		'零': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
	}
	runes := []rune(s)
	total, current := 0, 0
	for _, r := range runes {
		if r == '十' {
			if current == 0 {
				current = 1
			}
			total += current * 10
			current = 0
			continue
		}
		if v, ok := digits[r]; ok {
			current = v
			continue
		}
		return 0
	}
	return total + current
}

// seasonSubdirs 列出目录下所有「季」子目录的名字
func seasonSubdirs(ctx context.Context, dirPath string) []string {
	entries, err := listVFSDir(ctx, dirPath)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && isSeasonDir(e.GetName()) {
			out = append(out, e.GetName())
		}
	}
	return out
}

// promoteSeasonTarget 把「季」目录上移一层到剧名目录。
//
// /影视资源/群星/第一季、/影视资源/群星/第二季 若各自成条，同一部剧会被拆成
// 多条记录并重复刮削，而且拿「第一季」去搜也搜不到剧名。季目录本身代表不了
// 一部剧，所以统一上移，由剧名目录带递归选集入库（多个季落到同一条记录）。
// 父目录就是扫描根时不上移：那种布局里根目录是「库」，上移会把整个库并成一条。
func promoteSeasonTarget(target, scanRoot string, mediaType model.MediaType, pathMerge bool) (string, bool) {
	if !pathMerge || mediaType != model.MediaTypeVideo || !isSeasonDir(stdpath.Base(target)) {
		return target, false
	}
	parent := stdpath.Dir(target)
	if parent == "" || parent == "." || parent == "/" || parent == stdpath.Clean(scanRoot) {
		return target, false
	}
	return parent, true
}

// normalizeDirName 归一化目录名用于匹配：小写并去掉空格、点、下划线、连字符
func normalizeDirName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch r {
		case ' ', '\t', '.', '_', '-', '·':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

const (
	// mediaSaveBatchSize 每批落盘的条目数。扫描是流式的：每发现 mediaSaveBatchSize
	// 个条目就写一次库，避免「整棵树枚举完才开始落盘」——大库扫描时用户会长时间
	// 看不到任何条目。
	mediaSaveBatchSize = 20
	// mediaSaveBatchInterval 批量落盘的最长等待时间。目录很大、条目稀疏时靠它兜底，
	// 保证扫描过程中数据持续可见。
	mediaSaveBatchInterval = 3 * time.Second
	// maxMergeScanDepth 路径合并模式的最大递归深度，防止异常目录结构导致无边界递归。
	maxMergeScanDepth = 16
)

// videoTypeMovie / videoTypeTV 与 model.MediaItem.VideoType 的取值保持一致。
const (
	videoTypeMovie = "movie"
	videoTypeTV    = "tv"
)

// tvTypeTags 会被判定为「剧集」的类型标签（小写、去空格后比较）。
// 命中后该扫描路径下所有视频都会以 tv 入库，Emby 里会按剧集展示。
var tvTypeTags = map[string]bool{
	"电视剧": true, "剧集": true, "连续剧": true, "电视节目": true, "美剧": true,
	"日剧": true, "韩剧": true, "国产剧": true, "港剧": true, "台剧": true,
	"动漫": true, "番剧": true, "动画": true, "anime": true, "tvs": true,
	"纪录片": true, "documentary": true, "综艺": true, "variety": true,
	"tv": true, "tvshow": true, "tvshows": true, "series": true,
}

// movieTypeTags 会被判定为「电影」的类型标签。命中后即使开启了路径合并模式，
// 该路径下的文件夹条目也会按电影展示，而不是按剧集。
var movieTypeTags = map[string]bool{
	"电影": true, "影片": true, "movie": true, "movies": true, "films": true,
	"film": true, "演唱会": true, "concert": true,
}

// normalizeTypeTag 归一化类型标签，便于匹配预置选项（忽略大小写、空白和常见分隔符）。
func normalizeTypeTag(tag string) string {
	return strings.ToLower(strings.Join(strings.Fields(tag), ""))
}

// containsTypeKeyword 判断归一化后的类型标签里是否包含关键字。
//
// 纯英文关键字（tv / tvshows / movie…）要求命中位置左右都不是英文字母：
// 归一化会去掉空格，直接 strings.Contains 会让 "tv" 命中 "itvshows"、"mtv"
// 这类无关标签。中文关键字（电影、电视剧…）和「4K电影」这种复合标签仍按子串匹配。
func containsTypeKeyword(normalized, keyword string) bool {
	if !isASCIIWord(keyword) {
		return strings.Contains(normalized, keyword)
	}
	for start := 0; start < len(normalized); {
		pos := strings.Index(normalized[start:], keyword)
		if pos < 0 {
			return false
		}
		idx := start + pos
		end := idx + len(keyword)
		leftOK := idx == 0 || !isASCIILetter(normalized[idx-1])
		rightOK := end == len(normalized) || !isASCIILetter(normalized[end])
		if leftOK && rightOK {
			return true
		}
		start = idx + 1
	}
	return false
}

// isASCIIWord 判断关键字是否只由英文字母组成
func isASCIIWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isASCIILetter(s[i]) {
			return false
		}
	}
	return true
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// VideoTypeForTypeTag 根据扫描路径的类型标签推断视频条目类型，返回 "movie"、"tv"。
// 返回空字符串表示标签无法判定，此时沿用「文件夹按剧集、单文件按电影」的旧规则。
func VideoTypeForTypeTag(tag string) string {
	normalized := normalizeTypeTag(tag)
	if normalized == "" {
		return ""
	}
	if tvTypeTags[normalized] {
		return videoTypeTV
	}
	if movieTypeTags[normalized] {
		return videoTypeMovie
	}
	// 允许「电视剧(国产)」「4K 电影」这类复合标签：包含关键字即判定，剧集优先，
	// 因为把电影误判成剧集只会多一个层级，反过来则会让整季剧集散成一堆电影。
	for keyword := range tvTypeTags {
		if containsTypeKeyword(normalized, keyword) {
			return videoTypeTV
		}
	}
	for keyword := range movieTypeTags {
		if containsTypeKeyword(normalized, keyword) {
			return videoTypeMovie
		}
	}
	return ""
}

// ScanProgress 扫描进度（全局，按媒体类型维护）
type ScanProgress struct {
	mu      sync.RWMutex
	Running bool
	Total   int
	Done    int
	// Skipped 入库被跳过的条目数（典型原因：目录/文件名超过唯一索引列长度上限）。
	// 以前只在日志里打一条 Warn，用户完全看不到漏扫了多少条。
	Skipped int
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
	p.Skipped = 0
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
		if p.Skipped > 0 {
			// 让管理端能直接看到「有 N 条因为路径过长没入库」
			p.Message = fmt.Sprintf("扫描完成（跳过 %d 条）", p.Skipped)
			log.Warnf("media scan skipped %d items (path/name too long for unique index)", p.Skipped)
		} else {
			p.Message = "扫描完成"
		}
		p.mu.Unlock()
		// 更新媒体库最后扫描时间
		now := time.Now()
		cfg.LastScanAt = &now
		_ = db.SaveMediaConfig(cfg)

		// 按需生成 STRM
		TriggerStrmOnScan(cfg.MediaType, GetAPIRoot())
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
	p.Skipped = 0
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
			if p.Skipped > 0 {
				p.Message = fmt.Sprintf("扫描完成（跳过 %d 条）", p.Skipped)
				log.Warnf("media scan skipped %d items (path/name too long for unique index)", p.Skipped)
			} else {
				p.Message = "扫描完成"
			}
			p.mu.Unlock()
			now := time.Now()
			sp.LastScanAt = &now
			_ = db.UpdateMediaScanPath(sp)
		}
	}()
}

// mediaSaver 边扫描边落盘：条目先进入缓冲区，满 mediaSaveBatchSize 或者超过
// mediaSaveBatchInterval 就写一次数据库。这样扫描过程本身是可见的，不必等整棵树
// 枚举完毕。
type mediaSaver struct {
	mu   sync.Mutex
	buf  []*model.MediaItem
	last time.Time
	p    *ScanProgress
	// 定时落盘的守护协程。只在 Add 里检查间隔是不够的：扫描卡在慢速远端目录上时
	// 可能长时间没有新条目，缓冲区里的数据就一直不落盘。
	stop chan struct{}
	done chan struct{}
}

func newMediaSaver(p *ScanProgress) *mediaSaver {
	s := &mediaSaver{
		last: time.Now(),
		p:    p,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go s.loop()
	return s
}

// loop 按 mediaSaveBatchInterval 周期落盘
func (s *mediaSaver) loop() {
	defer close(s.done)
	t := time.NewTicker(mediaSaveBatchInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.Flush()
		}
	}
}

// Close 停止定时落盘并做最后一次 Flush（可重复调用）
func (s *mediaSaver) Close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	s.Flush()
}

// Add 把条目放入缓冲区，必要时立即落盘。
func (s *mediaSaver) Add(item *model.MediaItem) {
	if item == nil {
		return
	}
	s.mu.Lock()
	s.buf = append(s.buf, item)
	full := len(s.buf) >= mediaSaveBatchSize
	expired := time.Since(s.last) >= mediaSaveBatchInterval
	s.mu.Unlock()
	if full || expired {
		s.Flush()
	}
}

// Flush 把缓冲区写库并更新进度。
func (s *mediaSaver) Flush() {
	s.mu.Lock()
	items := s.buf
	s.buf = nil
	s.last = time.Now()
	s.mu.Unlock()
	if len(items) == 0 {
		return
	}
	var lastName string
	for _, item := range items {
		if err := db.CreateOrUpdateMediaItem(item); err != nil {
			log.Warnf("save media item error [%s/%s]: %v", item.FolderPath, item.FileName, err)
			s.p.mu.Lock()
			s.p.Skipped++
			s.p.mu.Unlock()
			continue
		}
		lastName = item.FileName
	}
	s.p.mu.Lock()
	s.p.Done += len(items)
	if lastName != "" {
		s.p.Message = fmt.Sprintf("已落盘 %d 条，最近: %s", s.p.Done, lastName)
	}
	s.p.mu.Unlock()
}

func doScanPath(sp *model.MediaScanPath, p *ScanProgress) error {
	scanRoot := sp.Path
	if scanRoot == "" {
		scanRoot = "/"
	}

	ctx := context.Background()
	saver := newMediaSaver(p)
	// 无论正常结束还是中途报错，都要停掉定时器并把缓冲区里的条目落盘
	defer saver.Close()

	// 每个扫描目标的处理逻辑：普通文件独立成条，合并模式的文件夹带选集信息
	// seen 记录已经入库的目标，避免「第一季」「第二季」上移到同一部剧后重复处理
	seen := make(map[string]bool)
	visit := func(target string) error {
		// 季目录（第一季 / Season 1 / S01）上移到剧名目录：一部剧只入库一条，
		// 播放时选集里按「第一季 / XX」区分
		target, promoted := promoteSeasonTarget(target, scanRoot, sp.MediaType, sp.PathMerge)
		if seen[target] {
			return nil
		}
		seen[target] = true

		p.mu.Lock()
		p.Total++
		p.Message = "正在扫描: " + stdpath.Base(target)
		p.mu.Unlock()

		if sp.MediaType == model.MediaTypeMusic {
			return saveMusicTarget(ctx, target, sp, p, saver)
		}

		item, err := buildMediaItemFromVFS(ctx, target, sp)
		if err != nil {
			log.Warnf("build media item error [%s]: %v", target, err)
			p.mu.Lock()
			p.Done++
			p.mu.Unlock()
			return nil
		}

		// 目录下带「第N季」子目录 → 这是剧集：即使扫描路径的标签写的是「电影」，
		// 也按剧集入库，Emby / 播放器的选集才能分出「第一季」「第二季」
		var seasons []string
		if sp.MediaType == model.MediaTypeVideo && sp.PathMerge && item.IsFolder {
			seasons = seasonSubdirs(ctx, target)
			// 扫描路径显式标成「电影」时一律按电影入库，不再被季目录反推成剧集；
			// 未打标签或标成剧集时才用季目录作为「这是剧集」的证据
			if len(seasons) > 0 && item.VideoType != videoTypeMovie {
				item.VideoType = videoTypeTV
			}
		}

		// 路径合并模式下，扫描文件夹内的文件，填充选集信息。
		// 固定深度、季目录上移、或目录本身按季组织时，内部还有「第N季」子目录，
		// 必须递归收集，否则深层文件不会出现在选集里（同一部剧只保留一条记录）。
		recursiveEpisodes := sp.MediaType == model.MediaTypeVideo &&
			(sp.MergeDepth > 0 || promoted || len(seasons) > 0)
		if sp.PathMerge && item.IsFolder {
			episodes, err := buildEpisodesFromFolder(ctx, target, sp.MediaType, recursiveEpisodes)
			if err != nil {
				log.Warnf("build episodes error [%s]: %v", target, err)
			}
			item.Episodes = episodes
			// 固定深度按层级直接取文件夹，可能命中没有任何媒体文件的空目录，
			// 不要为它们生成空条目
			if recursiveEpisodes && episodes == "" {
				p.mu.Lock()
				p.Done++
				p.mu.Unlock()
				return nil
			}
		}

		// 旧版本按季各成一条的记录（folder_path=剧名目录, file_name=第一季）会与
		// 当前这条重复，顺手清掉，避免同一部剧在媒体库里出现两次
		if len(seasons) > 0 {
			for _, name := range seasons {
				if _, err := db.DeleteMediaItemByPath(target, name); err != nil {
					log.Warnf("delete stale season item [%s/%s]: %v", target, name, err)
				}
			}
		}

		if err := mediaItemSizeCheck(item); err != nil {
			log.Warnf("skip media item [%s]: %v", target, err)
			p.mu.Lock()
			p.Skipped++
			p.Done++
			p.mu.Unlock()
			return nil
		}
		saver.Add(item)
		return nil
	}

	if sp.PathMerge {
		// 路径合并模式：从扫描根开始逐层向下找「真正装了媒体文件的文件夹」。
		//  - 某一层文件夹直接包含媒体文件 → 该文件夹作为一个合并条目（带 Episodes）
		//  - 该层没有任何媒体文件 → 继续深入下一层
		//  - 集合条目内部还有子目录（如「群星/群星2」）→ 子目录继续各自成条
		//  - 扫描根下直接散落的文件 → 各自作为独立条目
		//
		// MergeDepth > 0 时改为「固定层级」：直接把第 N 层的文件夹作为一个条目，
		// 不再按「是否直接包含媒体文件」判断。用于把 /电视剧/<剧名>/第N季/...
		// 聚合为同一部剧，避免每一季各成一条、重复刮削。
		if sp.MergeDepth > 0 && sp.MediaType == model.MediaTypeVideo {
			if err := walkMergeDepth(ctx, scanRoot, sp.MediaType, sp.MergeDepth, visit); err != nil {
				return err
			}
		} else if err := walkMergeFolders(ctx, scanRoot, sp.MediaType, 0, visit); err != nil {
			return err
		}
	} else {
		// 普通模式：递归扫描所有匹配文件（每个目录都刷新缓存）
		if err := walkVFS(ctx, scanRoot, sp.MediaType, visit); err != nil {
			return err
		}
	}
	return nil
}

// mediaItemSizeCheck 提前校验唯一索引列长度，避免把必然失败的行送进写库批次。
func mediaItemSizeCheck(item *model.MediaItem) error {
	if utf8.RuneCountInString(item.FolderPath) > model.MediaIndexColumnMax ||
		utf8.RuneCountInString(item.FileName) > model.MediaIndexColumnMax ||
		utf8.RuneCountInString(item.AlbumName) > model.MediaIndexColumnMax {
		return fmt.Errorf("path/name exceeds %d characters", model.MediaIndexColumnMax)
	}
	return nil
}

// walkMergeFolders 合并模式的逐层扫描。
//
// 规则（每一层都适用）：
//   - 该层直接包含媒体文件 → 本文件夹是一个有效集合，交给 visit 处理；
//     仍继续向下扫描子目录，因为集合目录里可能还嵌着别的影片
//     （如「群星/群星2」）。本条的选集只收本层文件，不会与子条目重复
//   - 该层没有媒体文件但有子目录 → 继续向下递归
//   - 花絮 / 字幕 / 样片这类附属目录整体跳过
//   - depth 为 0（扫描根本身）时，散落的媒体文件各自作为独立条目
func walkMergeFolders(ctx context.Context, dirPath string, mediaType model.MediaType, depth int, visit func(string) error) error {
	return walkMergeFoldersWith(ctx, listVFSDir, dirPath, mediaType, depth, visit)
}

// listDirFunc 列出一个 VFS 目录。抽成函数类型是为了让遍历规则可以脱离真实存储做单元测试。
type listDirFunc func(ctx context.Context, dirPath string) ([]model.Obj, error)

func listVFSDir(ctx context.Context, dirPath string) ([]model.Obj, error) {
	return fs.List(ctx, dirPath, &fs.ListArgs{NoLog: true, Refresh: true})
}

func walkMergeFoldersWith(ctx context.Context, list listDirFunc, dirPath string, mediaType model.MediaType, depth int, visit func(string) error) error {
	if depth > maxMergeScanDepth {
		log.Warnf("media scan: merge mode reached max depth %d at [%s], skipped", maxMergeScanDepth, dirPath)
		return nil
	}
	entries, err := list(ctx, dirPath)
	if err != nil {
		log.Warnf("media scan: list vfs path [%s] error: %v", dirPath, err)
		return nil // 跳过无权限目录，不中断整体扫描
	}

	// 第一遍：收集本层文件与子目录
	var files []string
	var dirs []string
	for _, e := range entries {
		childPath := stdpath.Join(dirPath, e.GetName())
		if e.IsDir() {
			dirs = append(dirs, childPath)
			continue
		}
		if isMediaFile(e.GetName(), mediaType) {
			files = append(files, childPath)
		}
	}

	// 扫描根下散落的文件：独立条目（保持旧行为）
	if depth == 0 {
		for _, file := range files {
			if err := visit(file); err != nil {
				return err
			}
		}
	} else if len(files) > 0 {
		// 子层文件夹直接装了媒体文件 → 它就是一个集合条目。
		// 这里不能 return：集合目录里还可能嵌着别的影片（例如
		// 「群星.1080p.内嵌中英双字/群星2」），提前返回会让整棵子目录漏扫。
		// 父子不会重复入库：本条只收本层文件，子目录各自成条。
		if err := visit(dirPath); err != nil {
			return err
		}
	}

	for _, sub := range dirs {
		// 花絮 / 字幕 / 样片这类附属目录不单独成条
		if isExtraMediaDir(stdpath.Base(sub)) {
			log.Debugf("media scan: skip extras dir [%s]", sub)
			continue
		}
		if err := walkMergeFoldersWith(ctx, list, sub, mediaType, depth+1, visit); err != nil {
			return err
		}
	}
	return nil
}

// walkMergeDepth 固定层级的合并扫描（MediaScanPath.MergeDepth）。
//
// MergeDepth 的含义是「扫描根往下第 N 层目录作为一条记录」，
// 扫描根的直接子文件夹就是第 1 层：
//
//	0 = 自动（调用方走 walkMergeFolders）
//	1 = /电视剧/群星 作为一条（剧名层，最常用）
//	2 = /电视剧/群星/第一季 各自成条（季目录仍会被 promoteSeasonTarget 上移回剧名）
//
// 内部 curDepth 从扫描根算起，扫描根自身是第 0 层，正好等于目标层 depth。
func walkMergeDepth(ctx context.Context, dirPath string, mediaType model.MediaType, depth int, visit func(string) error) error {
	if depth <= 0 {
		// 0 表示自动模式，调用方应该走 walkMergeFolders；这里兜底避免死循环
		return walkMergeFolders(ctx, dirPath, mediaType, 0, visit)
	}
	if depth > maxMergeScanDepth {
		depth = maxMergeScanDepth
	}
	return walkMergeDepthWith(ctx, listVFSDir, dirPath, mediaType, depth, 0, visit)
}

func walkMergeDepthWith(ctx context.Context, list listDirFunc, dirPath string, mediaType model.MediaType, targetDepth, curDepth int, visit func(string) error) error {
	if curDepth >= targetDepth {
		// 目标层级：整棵子树归入这一条。没有任何媒体文件的目录由 visit 内部跳过。
		return visit(dirPath)
	}
	entries, err := list(ctx, dirPath)
	if err != nil {
		log.Warnf("media scan: list vfs path [%s] error: %v", dirPath, err)
		return nil // 跳过无权限目录，不中断整体扫描
	}

	var dirs []string
	for _, e := range entries {
		childPath := stdpath.Join(dirPath, e.GetName())
		if e.IsDir() {
			dirs = append(dirs, childPath)
			continue
		}
		// 扫描根下散落的媒体文件：各自独立成条（与自动模式保持一致）
		if curDepth == 0 && isMediaFile(e.GetName(), mediaType) {
			if err := visit(childPath); err != nil {
				return err
			}
		}
	}

	if len(dirs) == 0 {
		// 层级还没到目标深度，但已经没有子目录可下钻：当前目录就是这一条，
		// 否则这种「层级不够」的库会一条都扫不出来。
		if curDepth > 0 {
			return visit(dirPath)
		}
		return nil
	}

	for _, sub := range dirs {
		// 花絮 / 字幕 / 样片这类附属目录不参与成条
		if isExtraMediaDir(stdpath.Base(sub)) {
			continue
		}
		if err := walkMergeDepthWith(ctx, list, sub, mediaType, targetDepth, curDepth+1, visit); err != nil {
			return err
		}
	}
	return nil
}

// collectMusicEntries 从目录条目里挑出音乐文件并生成选集。
//
// 序号按「第几个音乐文件」计算，不能直接用目录条目的下标：专辑目录里经常混有
// cover.jpg、readme.txt 之类的非音频文件，用下标会让曲目号跳号（1、3、4…）。
func collectMusicEntries(dirPath string, entries []model.Obj, mediaType model.MediaType) ([]string, []EpisodeInfo) {
	var files []string
	var episodes []EpisodeInfo
	for _, e := range entries {
		if e.IsDir() || !isMediaFile(e.GetName(), mediaType) {
			continue
		}
		files = append(files, stdpath.Join(dirPath, e.GetName()))
		episodes = append(episodes, EpisodeInfo{
			FileName: e.GetName(),
			Index:    len(episodes) + 1,
			Title:    strings.TrimSuffix(e.GetName(), stdpath.Ext(e.GetName())),
		})
	}
	return files, episodes
}

// saveMusicTarget 音乐扫描（单个目标）
// 存储规则：
//   - 普通文件（is_folder=false）：
//     folder_path=文件所在真实目录，file_name=音乐文件名，episodes=空
//   - 合并文件夹（is_folder=true）：
//     folder_path=文件夹所在真实父目录，file_name=文件夹名，
//     episodes=文件夹内所有音乐文件列表，封面/标签取第一首
func saveMusicTarget(ctx context.Context, target string, sp *model.MediaScanPath, p *ScanProgress, saver *mediaSaver) error {
	obj, err := fs.Get(ctx, target, &fs.GetArgs{NoLog: true})
	if err != nil {
		log.Warnf("get vfs object error [%s]: %v", target, err)
		p.mu.Lock()
		p.Done++
		p.mu.Unlock()
		return nil
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
			return nil
		}

		musicFiles, episodes := collectMusicEntries(target, entries, sp.MediaType)

		if len(musicFiles) == 0 {
			p.mu.Lock()
			p.Done++
			p.mu.Unlock()
			return nil
		}

		// 取第一首音乐文件读取标签（关闭「读取文件信息」时不读，退回目录名）
		firstFile := musicFiles[0]
		firstExt := strings.ToLower(stdpath.Ext(firstFile))
		var tag *MusicTag
		if !SkipFileMetadata() {
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
		}

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

		// 加载歌词：同目录同名 .lrc 优先，其次内嵌歌词
		lyrics := loadLyricsForMusic(ctx, firstFile, tag)

		item := &model.MediaItem{
			MediaType:   sp.MediaType,
			ScanPathID:  sp.ID,
			FileName:    name,                // 文件夹名
			FolderPath:  stdpath.Dir(target), // 文件夹所在的真实父目录
			IsFolder:    true,
			AlbumName:   albumName,
			AlbumArtist: albumArtist,
			ScrapedName: albumName,
			Cover:       cover,
			ReleaseDate: releaseDate,
			Genre:       genre,
			Authors:     authors,
			Lyrics:      lyrics,
			Episodes:    episodesJSON,
		}

		if err := mediaItemSizeCheck(item); err != nil {
			log.Warnf("skip music folder item [%s]: %v", target, err)
			p.mu.Lock()
			p.Skipped++
			p.Done++
			p.mu.Unlock()
		} else {
			saver.Add(item)
		}
	} else {
		// ---- 普通文件：每首歌独立一条记录 ----
		folderPath := stdpath.Dir(target)
		ext := strings.ToLower(stdpath.Ext(name))

		var tag *MusicTag
		if !SkipFileMetadata() {
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
		}

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

		// 加载歌词：同目录同名 .lrc 优先，其次内嵌歌词
		lyrics := loadLyricsForMusic(ctx, target, tag)

		item := &model.MediaItem{
			MediaType:   sp.MediaType,
			ScanPathID:  sp.ID,
			FileName:    name,
			FolderPath:  folderPath,
			IsFolder:    false,
			FileSize:    obj.GetSize(),
			MimeType:    mediaMimeType(ext),
			AlbumName:   albumName,
			AlbumArtist: albumArtist,
			ScrapedName: trackTitle,
			Cover:       cover,
			ReleaseDate: releaseDate,
			Genre:       genre,
			Authors:     authors,
			TrackNumber: trackNumber,
			Lyrics:      lyrics,
		}

		if err := mediaItemSizeCheck(item); err != nil {
			log.Warnf("skip music item [%s]: %v", target, err)
			p.mu.Lock()
			p.Skipped++
			p.Done++
			p.mu.Unlock()
		} else {
			saver.Add(item)
		}
	}

	return nil
}

// loadLyricsForMusic 为音乐文件加载歌词：
//  1. 优先读取同目录同名 .lrc 文件（如 song.mp3 → song.lrc）
//  2. 如果不存在，使用 tag 中的内嵌歌词（USLT / Vorbis LYRICS）
//
// musicVfsPath 为音乐文件的 VFS 全路径；tag 可为 nil
func loadLyricsForMusic(ctx context.Context, musicVfsPath string, tag *MusicTag) string {
	// 尝试同目录同名 .lrc
	ext := stdpath.Ext(musicVfsPath)
	if ext != "" {
		lrcPath := strings.TrimSuffix(musicVfsPath, ext) + ".lrc"
		lrcCtx, lrcCancel := context.WithTimeout(ctx, 8*time.Second)
		reader := FetchFileReader(lrcCtx, lrcPath)
		if reader != nil {
			// 限制读取上限 1MB，避免异常文件压垮内存
			data, err := io.ReadAll(io.LimitReader(reader, 1024*1024))
			_ = reader.Close()
			lrcCancel()
			if err == nil && len(data) > 0 {
				// 去掉可能的 UTF-8 BOM
				if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
					data = data[3:]
				}
				s := strings.TrimSpace(string(data))
				if s != "" {
					return s
				}
			}
		} else {
			lrcCancel()
		}
	}
	// 回退到内嵌歌词
	if tag != nil && tag.Lyrics != "" {
		return tag.Lyrics
	}
	return ""
}

// FetchFileReader 通过 VFS 路径获取文件内容流（用于刮削器读取文件内容）
// 优先使用 RangeReader 直接读取（本地存储无需 HTTP），失败时回退到 HTTP URL
// 返回 nil 表示无法获取（不影响主流程）
func FetchFileReader(ctx context.Context, vfsPath string) io.ReadCloser {
	link, file, err := fs.Link(ctx, vfsPath, model.LinkArgs{})
	if err != nil || link == nil {
		return nil
	}
	size := link.ContentLength
	if size <= 0 {
		size = file.GetSize()
	}
	rr, err := stream.GetRangeReaderFromLink(size, link)
	if err != nil {
		_ = link.Close()
		return nil
	}
	rc, err := rr.RangeRead(ctx, http_range.Range{Length: size})
	if err != nil {
		_ = link.Close()
		return nil
	}
	link.SyncClosers.Add(rc)
	return utils.ReadCloser{Reader: rc, Closer: link}
}

// walkVFS 递归遍历 VFS 路径，收集匹配的媒体文件路径（每个目录都刷新缓存）
func walkVFS(ctx context.Context, dirPath string, mediaType model.MediaType, visit func(string) error) error {
	entries, err := fs.List(ctx, dirPath, &fs.ListArgs{NoLog: true, Refresh: true})
	if err != nil {
		log.Warnf("media scan: list vfs path [%s] error: %v", dirPath, err)
		return nil // 跳过无权限目录，不中断整体扫描
	}
	for _, e := range entries {
		childPath := stdpath.Join(dirPath, e.GetName())
		if e.IsDir() {
			if err := walkVFS(ctx, childPath, mediaType, visit); err != nil {
				return err
			}
		} else if isMediaFile(e.GetName(), mediaType) {
			// 边枚举边处理：条目发现一个就交给 visit 处理一个（内部按批落盘），
			// 不再先攒全量列表，避免大库扫描时长时间没有任何数据写入。
			if err := visit(childPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildMediaItemFromVFS 根据 VFS 路径构建 MediaItem（非音乐类型使用）
// 存储规则：
//   - folder_path：文件/文件夹所在的真实父目录（VFS 父路径），用于前端按 folder_path + file_name 反查真实文件
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
		MediaType:   sp.MediaType,
		ScanPathID:  sp.ID,
		FileName:    name,                 // 文件夹名 或 文件名
		FolderPath:  stdpath.Dir(vfsPath), // 真实父目录
		IsFolder:    obj.IsDir(),
		ScrapedName: strings.TrimSuffix(name, stdpath.Ext(name)), // 去掉扩展名作为默认名称
	}

	// 类型标签决定电影/剧集归属：扫描路径被打成「电视剧」时，下属视频（包括
	// 单文件）都以 tv 入库，Emby 会按剧集展示；打成「电影」时即使开着路径合并
	// 也按电影展示。标签无法判定时保持空，沿用旧规则（文件夹按剧集）。
	if sp.MediaType == model.MediaTypeVideo {
		item.VideoType = VideoTypeForTypeTag(sp.TypeTag)
	}

	if !obj.IsDir() {
		// 普通文件：记录大小和 MIME 类型，episodes 为空
		item.FileSize = obj.GetSize()
		item.MimeType = mediaMimeType(ext)
	}

	return item, nil
}

// episodeNumRe 匹配文件名开头的数字序号，支持 "1、" "2." "3-" "4 " 等分隔符
var episodeNumRe = regexp.MustCompile(`^(\d+)[、.\-\s_]+(.*)`)

// EpisodeInfo 选集信息（定义在 model 包中，供协议层复用）
type EpisodeInfo = model.EpisodeInfo

// buildEpisodeInfo 从文件名解析选集信息（序号 + 去掉序号和扩展名的标题）
func buildEpisodeInfo(name string) EpisodeInfo {
	// 去掉扩展名得到裸文件名
	baseName := strings.TrimSuffix(name, stdpath.Ext(name))
	ep := EpisodeInfo{FileName: name, Index: 0, Title: baseName}

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
	return ep
}

// buildEpisodesFromFolder 扫描文件夹内的媒体文件，构建选集信息 JSON 字符串。
// recursive=true 时递归子目录，标题加上「子目录/」前缀：固定深度的剧集条目
// 需要它把各季目录下的文件都收进同一条记录，同时区分重复的集号。
func buildEpisodesFromFolder(ctx context.Context, folderPath string, mediaType model.MediaType, recursive bool) (string, error) {
	return buildEpisodesFromFolderWith(ctx, listVFSDir, folderPath, mediaType, recursive)
}

func buildEpisodesFromFolderWith(ctx context.Context, list listDirFunc, folderPath string, mediaType model.MediaType, recursive bool) (string, error) {
	var episodes []EpisodeInfo

	// season 记录当前所在季目录的季号，供前端 / Emby 按季分组显示
	var walk func(dir, rel string, season, depth int) error
	walk = func(dir, rel string, season, depth int) error {
		entries, err := list(ctx, dir)
		if err != nil {
			if depth == 0 {
				return err
			}
			// 子目录读不到（无权限/驱动抖动）不影响其余选集
			log.Warnf("media scan: list [%s] error: %v", dir, err)
			return nil
		}
		for _, e := range entries {
			name := e.GetName()
			if e.IsDir() {
				// 附属目录（花絮 / 字幕 / 样片）不算选集，避免选集列表里混入无关片段
				if !recursive || depth >= maxMergeScanDepth || isExtraMediaDir(name) {
					continue
				}
				subRel := name
				if rel != "" {
					subRel = stdpath.Join(rel, name)
				}
				// 子目录是「第一季 / Season 1 / S01」时记录季号，更深层的目录沿用
				subSeason := season
				if n := seasonNumberOf(name); n > 0 {
					subSeason = n
				}
				if err := walk(stdpath.Join(dir, name), subRel, subSeason, depth+1); err != nil {
					return err
				}
				continue
			}
			if !isMediaFile(name, mediaType) {
				continue
			}
			ep := buildEpisodeInfo(name)
			ep.Season = season
			if rel != "" {
				// 文件在子目录里：file_name 必须带上相对路径。
				// 播放地址是按 folder_path/file_name/选集.file_name 拼的，
				// 缺了「第一季/」这一层就会请求到不存在的文件（500/404）。
				ep.FileName = stdpath.Join(rel, name)
				ep.Title = rel + " / " + ep.Title
			}
			episodes = append(episodes, ep)
		}
		return nil
	}

	if err := walk(folderPath, "", 0, 0); err != nil {
		return "", err
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

// mediaMimeType 返回媒体文件的 MIME 类型。
// Go 内置表不一定包含 .iso，显式补齐，避免蓝光/DVD 原盘入库后 mime_type 为空。
func mediaMimeType(ext string) string {
	if ext == ".iso" {
		return "application/x-iso9660-image"
	}
	return mime.TypeByExtension(ext)
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
