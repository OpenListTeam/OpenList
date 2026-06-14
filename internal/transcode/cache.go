package transcode

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SegmentInfo 单个切片元数据
type SegmentInfo struct {
	Seq      int     `json:"seq"`
	Duration float64 `json:"duration"`
	Size     int64   `json:"size"`
	Path     string  `json:"-"` // 本地实际文件路径
}

// JobCache 单个 Job 的切片缓存
type JobCache struct {
	JobID   string
	Profile string
	BaseDir string

	mu       sync.RWMutex
	segments map[int]*SegmentInfo
	final    bool                    // 是否所有切片都到齐
	waiters  map[int][]chan struct{} // seq -> 等待这一片的 channel 列表
}

// Cache 全局切片缓存
type Cache struct {
	rootDir   string
	maxBytes  int64
	mu        sync.RWMutex
	jobs      map[string]map[string]*JobCache // jobID -> profile -> cache
}

func NewCache(rootDir string, maxGB int64) *Cache {
	if maxGB <= 0 {
		maxGB = 20
	}
	return &Cache{
		rootDir:  rootDir,
		maxBytes: maxGB * 1024 * 1024 * 1024,
		jobs:     make(map[string]map[string]*JobCache),
	}
}

// Init 创建根目录
func (c *Cache) Init() error {
	return os.MkdirAll(c.rootDir, 0o755)
}

// getOrCreate 获取或创建 (job, profile) 的 cache
func (c *Cache) getOrCreate(jobID, profile string) (*JobCache, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pmap, ok := c.jobs[jobID]
	if !ok {
		pmap = make(map[string]*JobCache)
		c.jobs[jobID] = pmap
	}
	jc, ok := pmap[profile]
	if !ok {
		jc = &JobCache{
			JobID:    jobID,
			Profile:  profile,
			BaseDir:  filepath.Join(c.rootDir, jobID, profile),
			segments: make(map[int]*SegmentInfo),
			waiters:  make(map[int][]chan struct{}),
		}
		if err := os.MkdirAll(jc.BaseDir, 0o755); err != nil {
			return nil, err
		}
		pmap[profile] = jc
	}
	return jc, nil
}

// PutSegment 写入一个切片，并通知所有等待者
func (c *Cache) PutSegment(jobID, profile string, seq int, duration float64, body io.Reader, isFinal bool) (*SegmentInfo, error) {
	jc, err := c.getOrCreate(jobID, profile)
	if err != nil {
		return nil, err
	}
	fname := fmt.Sprintf("seg-%d.ts", seq)
	fpath := filepath.Join(jc.BaseDir, fname)
	tmpPath := fpath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, body)
	cerr := f.Close()
	if err != nil {
		os.Remove(tmpPath)
		return nil, err
	}
	if cerr != nil {
		os.Remove(tmpPath)
		return nil, cerr
	}
	if err := os.Rename(tmpPath, fpath); err != nil {
		os.Remove(tmpPath)
		return nil, err
	}
	info := &SegmentInfo{Seq: seq, Duration: duration, Size: n, Path: fpath}

	jc.mu.Lock()
	jc.segments[seq] = info
	if isFinal {
		jc.final = true
	}
	// 通知所有等待 <= seq 的 waiter
	var toClose []chan struct{}
	for s, chs := range jc.waiters {
		if s <= seq {
			toClose = append(toClose, chs...)
			delete(jc.waiters, s)
		}
	}
	jc.mu.Unlock()
	for _, ch := range toClose {
		close(ch)
	}
	return info, nil
}

// GetSegment 读取一个切片（不等待）
func (c *Cache) GetSegment(jobID, profile string, seq int) (*SegmentInfo, error) {
	c.mu.RLock()
	pmap, ok := c.jobs[jobID]
	c.mu.RUnlock()
	if !ok {
		return nil, errors.New("job not found")
	}
	jc, ok := pmap[profile]
	if !ok {
		return nil, errors.New("profile not found")
	}
	jc.mu.RLock()
	defer jc.mu.RUnlock()
	info, ok := jc.segments[seq]
	if !ok {
		return nil, errors.New("segment not found")
	}
	return info, nil
}

// WaitForSegment 等待某个 seq 切片就绪，timeout 后返回 nil
func (c *Cache) WaitForSegment(jobID, profile string, seq int, timeout time.Duration) (*SegmentInfo, error) {
	jc, err := c.getOrCreate(jobID, profile)
	if err != nil {
		return nil, err
	}
	jc.mu.Lock()
	if info, ok := jc.segments[seq]; ok {
		jc.mu.Unlock()
		return info, nil
	}
	if jc.final {
		jc.mu.Unlock()
		return nil, errors.New("segment never produced")
	}
	ch := make(chan struct{})
	jc.waiters[seq] = append(jc.waiters[seq], ch)
	jc.mu.Unlock()
	select {
	case <-ch:
		jc.mu.RLock()
		info := jc.segments[seq]
		jc.mu.RUnlock()
		if info == nil {
			return nil, errors.New("segment not produced")
		}
		return info, nil
	case <-time.After(timeout):
		// 【内存泄漏修复】超时后必须从 waiters map 中移除自己的 channel，
		// 否则 channel 会永远留在 map 中无法被 GC
		jc.mu.Lock()
		if chs, ok := jc.waiters[seq]; ok {
			for i, w := range chs {
				if w == ch {
					jc.waiters[seq] = append(chs[:i], chs[i+1:]...)
					break
				}
			}
			if len(jc.waiters[seq]) == 0 {
				delete(jc.waiters, seq)
			}
		}
		jc.mu.Unlock()
		return nil, errors.New("timeout waiting segment")
	}
}

// SegmentCount 返回 (jobID, profile) 当前已生成的切片数量。
// 用于 TCPlaylist 在首次返回前等待至少若干切片就绪，避免 playlist
// 中只有 seg-0 导致 HLS.js 反复请求填充缓冲。
func (c *Cache) SegmentCount(jobID, profile string) int {
	c.mu.RLock()
	pmap, ok := c.jobs[jobID]
	c.mu.RUnlock()
	if !ok {
		return 0
	}
	jc, ok := pmap[profile]
	if !ok {
		return 0
	}
	jc.mu.RLock()
	defer jc.mu.RUnlock()
	return len(jc.segments)
}

// BuildPlaylist 生成 HLS playlist (m3u8)
// 【重要】当 totalDuration > 0 时，会根据总时长预先生成完整切片列表（VOD 模式），
// 即使切片还未生成，URL 也会出现在 playlist 中。HLS.js 请求未生成切片时
// 会阻塞等待（见 TCSegment 的 WaitForSegment）。这样可以让 HLS.js 知道
// 视频总时长，避免：
//  1. 时长显示错误（只显示已转码部分）
//  2. 拖动跳跃（playlist 增长导致切片序号重新对齐）
func (c *Cache) BuildPlaylist(jobID, profile string, segDur int, completed bool, baseURL string, totalDuration float64) string {
	c.mu.RLock()
	pmap, ok := c.jobs[jobID]
	c.mu.RUnlock()
	if !ok {
		return ""
	}
	jc, ok := pmap[profile]
	if !ok {
		return ""
	}
	jc.mu.RLock()
	defer jc.mu.RUnlock()

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	b.WriteString("#EXT-X-TARGETDURATION:" + strconv.Itoa(segDur) + "\n")
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")

	// 如果有总时长，按总时长生成完整切片列表
	if totalDuration > 0 {
		// VOD 模式：完整列表，HLS.js 知道总时长
		b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
		fSegDur := float64(segDur)
		// 总切片数 = ceil(总时长 / 每片时长)
		totalSegs := int(totalDuration / fSegDur)
		lastDur := totalDuration - float64(totalSegs)*fSegDur
		if lastDur > 0.01 {
			totalSegs++
		}
		for i := 0; i < totalSegs; i++ {
			dur := fSegDur
			if i == totalSegs-1 && lastDur > 0.01 {
				dur = lastDur
			}
			// 优先使用真实时长（如果已知）
			if seg, ok := jc.segments[i]; ok && seg.Duration > 0 {
				dur = seg.Duration
			}
			b.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", dur))
			b.WriteString(fmt.Sprintf("%sseg-%d.ts\n", baseURL, i))
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		return b.String()
	}

	// fallback: 没有总时长时退回到原 EVENT 模式（保留旧行为）
	seqs := make([]int, 0, len(jc.segments))
	for s := range jc.segments {
		seqs = append(seqs, s)
	}
	sort.Ints(seqs)
	if jc.final || completed {
		b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	} else {
		b.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
	}
	for _, s := range seqs {
		seg := jc.segments[s]
		b.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", seg.Duration))
		b.WriteString(fmt.Sprintf("%sseg-%d.ts\n", baseURL, s))
	}
	if jc.final || completed {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.String()
}

// MasterPlaylist 多档位主 m3u8
func (c *Cache) MasterPlaylist(profiles []Profile, baseURL string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
	for _, p := range profiles {
		bw, _ := parseBitrate(p.VideoBitrate)
		ab, _ := parseBitrate(p.AudioBitrate)
		// 【关键修复】根据实际编码动态生成 codec 字符串
		// 错误的 codec 声明会导致 HLS.js 用错误参数初始化 SourceBuffer
		// 然后 appendBuffer 时数据格式不匹配 → bufferAppendError 死循环
		codecs := buildCodecString(p)
		b.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=\"%s\"\n", bw+ab, codecs))
		b.WriteString(fmt.Sprintf("%s%s/playlist.m3u8\n", baseURL, p.Name))
	}
	return b.String()
}

// buildCodecString 根据 Profile 生成 HLS CODECS 属性字符串
// avc1.42E01E = H.264 Baseline Profile Level 3.0（兼容性最好）
// avc1.4D401E = H.264 Main Profile Level 3.0
// avc1.640028 = H.264 High Profile Level 4.0
// mp4a.40.2 = AAC-LC（HE-AAC v1 用 mp4a.40.5，HE-AAC v2 用 mp4a.40.29）
func buildCodecString(p Profile) string {
	// 视频部分：默认 H.264 Main Profile Level 4.0，平衡兼容性与质量
	video := "avc1.4D4028"
	switch strings.ToLower(p.VideoCodec) {
	case "hevc", "h265":
		// HEVC Main Profile Level 4.1，部分 Safari/Edge 支持
		video = "hvc1.1.6.L120.90"
	case "av1":
		video = "av01.0.05M.08"
	}
	// 音频部分：固定 AAC-LC（后端已强制转 AAC）
	audio := "mp4a.40.2"
	return video + "," + audio
}

func parseBitrate(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	if strings.HasSuffix(s, "k") {
		mult = 1000
		s = strings.TrimSuffix(s, "k")
	} else if strings.HasSuffix(s, "m") {
		mult = 1_000_000
		s = strings.TrimSuffix(s, "m")
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return v * mult, nil
}

// Cleanup 删除某个 job 的所有切片，并关闭所有阻塞的 waiter goroutine
func (c *Cache) Cleanup(jobID string) {
	c.mu.Lock()
	pmap, exists := c.jobs[jobID]
	delete(c.jobs, jobID)
	c.mu.Unlock()
	// 【内存泄漏修复】关闭所有 profile 下阻塞等待的 waiter channel，
	// 避免 goroutine 永远挂起（每个 goroutine 至少 8KB 栈空间）
	if exists {
		for _, jc := range pmap {
			jc.mu.Lock()
			jc.final = true // 标记为 final，防止新 waiter 进入
			for seq, chs := range jc.waiters {
				for _, ch := range chs {
					close(ch)
				}
				delete(jc.waiters, seq)
			}
			jc.mu.Unlock()
		}
	}
	_ = os.RemoveAll(filepath.Join(c.rootDir, jobID))
}

// EnforceLRU 简易 LRU 容量控制：超过 maxBytes 时按目录修改时间从早到晚删除
func (c *Cache) EnforceLRU() error {
	entries, err := os.ReadDir(c.rootDir)
	if err != nil {
		return err
	}
	type item struct {
		dir   string
		size  int64
		mtime time.Time
	}
	var items []item
	var total int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		full := filepath.Join(c.rootDir, e.Name())
		size := dirSize(full)
		fi, _ := os.Stat(full)
		mt := time.Now()
		if fi != nil {
			mt = fi.ModTime()
		}
		items = append(items, item{full, size, mt})
		total += size
	}
	if total <= c.maxBytes {
		return nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mtime.Before(items[j].mtime) })
	for _, it := range items {
		if total <= c.maxBytes {
			break
		}
		_ = os.RemoveAll(it.dir)
		total -= it.size
		// 同步从内存里删
		jobID := filepath.Base(it.dir)
		c.mu.Lock()
		delete(c.jobs, jobID)
		c.mu.Unlock()
	}
	return nil
}

func dirSize(dir string) int64 {
	var size int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size
}
