package transcode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// limitedBuffer 是一个有容量上限的 io.Writer，超出 maxSize 后静默丢弃。
// 用于捕获 ffmpeg stderr，防止异常文件导致无限内存增长。
type limitedBuffer struct {
	data    []byte
	maxSize int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remain := b.maxSize - len(b.data)
	if remain <= 0 {
		return len(p), nil // 静默丢弃
	}
	if len(p) > remain {
		p = p[:remain]
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	return string(b.data)
}

// LocalRunner 本地内置 worker，直接调用本机 ffmpeg
type LocalRunner struct {
	mgr      *Manager
	workerID string

	stopCh chan struct{}
	wg     sync.WaitGroup

	// 运行中 job 的 cancel 管理：jobID -> cancelFunc
	runMu       sync.Mutex
	runningJobs map[string]context.CancelFunc
}

func NewLocalRunner(mgr *Manager) *LocalRunner {
	return &LocalRunner{
		mgr:         mgr,
		stopCh:      make(chan struct{}),
		runningJobs: make(map[string]context.CancelFunc),
	}
}

// Start 注册本地 worker 并启动消费协程
func (r *LocalRunner) Start() error {
	concurrency := setting.GetInt(conf.TranscodeLocalConcurrency, 1)
	if concurrency <= 0 {
		concurrency = 1
	}
	hwaccel := setting.GetStr(conf.TranscodeHWAccel, "none")
	hostname, _ := os.Hostname()
	w := r.mgr.Registry.Register(&RegisterRequest{
		Name:          "local-" + hostname,
		Version:       "embedded",
		Capacity:      concurrency,
		HWAccel:       []string{hwaccel},
		CodecsDecode:  []string{"h264", "hevc", "av1", "vp9", "mpeg4", "mpeg2video"},
		CodecsEncode:  []string{"h264", "hevc"},
		MaxResolution: "3840x2160",
		Tags:          []string{"local", "embedded"},
	}, true)
	r.workerID = w.ID
	utils.Log.Infof("[transcode] local worker started: id=%s capacity=%d hwaccel=%s", w.ID, concurrency, hwaccel)

	for i := 0; i < concurrency; i++ {
		r.wg.Add(1)
		go r.loop()
	}
	return nil
}

func (r *LocalRunner) Stop() {
	close(r.stopCh)
	// 取消所有运行中的 FFmpeg 进程
	r.runMu.Lock()
	for id, cancel := range r.runningJobs {
		utils.Log.Infof("[transcode] stopping ffmpeg for job %s (runner shutdown)", id)
		cancel()
	}
	r.runMu.Unlock()
	r.wg.Wait()
	r.mgr.Registry.Unregister(r.workerID)
}

// CancelJob 外部取消指定 job 的 FFmpeg 进程（由 Scheduler.Cancel / 空闲超时调用）
func (r *LocalRunner) CancelJob(jobID string) bool {
	r.runMu.Lock()
	cancel, ok := r.runningJobs[jobID]
	r.runMu.Unlock()
	if ok {
		utils.Log.Infof("[transcode] killing ffmpeg for job %s (external cancel)", jobID)
		cancel()
	}
	return ok
}

// RunningJobIDs 返回当前正在运行的所有 job ID 列表
func (r *LocalRunner) RunningJobIDs() []string {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	ids := make([]string, 0, len(r.runningJobs))
	for id := range r.runningJobs {
		ids = append(ids, id)
	}
	return ids
}

func (r *LocalRunner) loop() {
	defer r.wg.Done()
	for {
		select {
		case <-r.stopCh:
			return
		default:
		}
		jobs := r.mgr.Scheduler.Claim(r.workerID, 1, 5*time.Second)
		if len(jobs) == 0 {
			continue
		}
		for _, j := range jobs {
			r.runJob(j)
		}
	}
}

// runJob 执行一个转码任务（单 profile，HLS 输出，边转边写入 cache）
func (r *LocalRunner) runJob(job *Job) {
	utils.Log.Infof("[transcode] local runner picked job %s (%s)", job.ID, job.Path)
	if len(job.Profiles) == 0 {
		_ = r.mgr.Scheduler.Finish(&FinishRequest{JobID: job.ID, Status: JobFailed, Error: "no profiles"})
		return
	}
	profile := job.Profiles[0]
	timeoutMin := setting.GetInt(conf.TranscodeJobTimeoutMin, 120)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMin)*time.Minute)
	defer cancel()

	// 注册到 runningJobs，支持外部取消
	r.runMu.Lock()
	r.runningJobs[job.ID] = cancel
	r.runMu.Unlock()
	defer func() {
		r.runMu.Lock()
		delete(r.runningJobs, job.ID)
		r.runMu.Unlock()
	}()

	// 输出目录
	jc, err := r.mgr.Cache.getOrCreate(job.ID, profile.Name)
	if err != nil {
		_ = r.mgr.Scheduler.Finish(&FinishRequest{JobID: job.ID, Status: JobFailed, Error: err.Error()})
		return
	}
	outDir := jc.BaseDir
	playlistPath := filepath.Join(outDir, "playlist.m3u8")
	segPattern := filepath.Join(outDir, "seg-%d.ts")

	args := buildFFmpegArgs(job, profile, segPattern, playlistPath)
	ffmpegPath := setting.GetStr(conf.TranscodeFFmpegPath, "ffmpeg")

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	// 【内存泄漏修复】限制 stderr 捕获量，避免 ffmpeg 输出大量 warning 导致内存暴涨
	stderr := &limitedBuffer{maxSize: 64 * 1024} // 最多 64KB
	cmd.Stderr = stderr

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		_ = r.mgr.Scheduler.Finish(&FinishRequest{JobID: job.ID, Status: JobFailed, Error: err.Error()})
		return
	}

	// 监控目录中新生成的切片
	stopWatcher := make(chan struct{})
	var watcherWG sync.WaitGroup
	watcherWG.Add(1)
	go func() {
		defer watcherWG.Done()
		r.watchSegments(job, profile, outDir, stopWatcher)
	}()

	err = cmd.Wait()
	close(stopWatcher)
	watcherWG.Wait()
	// 处理 m3u8 末尾，扫一次确保最后一片登记
	r.scanAndPublish(job, profile, outDir, true)

	if err != nil {
		// 区分是被外部取消还是 FFmpeg 自身失败
		if ctx.Err() != nil {
			utils.Log.Infof("[transcode] job %s ffmpeg killed (context cancelled)", job.ID)
			_ = r.mgr.Scheduler.Finish(&FinishRequest{
				JobID:  job.ID,
				Status: JobCancelled,
				Error:  "cancelled: " + ctx.Err().Error(),
			})
		} else {
			_ = r.mgr.Scheduler.Finish(&FinishRequest{
				JobID:  job.ID,
				Status: JobFailed,
				Error:  fmt.Sprintf("ffmpeg failed: %v, stderr=%s", err, truncate(stderr.String(), 1024)),
			})
		}
		return
	}
	utils.Log.Infof("[transcode] job %s completed in %.1fs", job.ID, time.Since(startedAt).Seconds())
	_ = r.mgr.Scheduler.Finish(&FinishRequest{
		JobID:  job.ID,
		Status: JobFinished,
		Stats:  JobStats{Elapsed: time.Since(startedAt).Seconds()},
	})
}

// watchSegments 周期性扫描输出目录，把新切片登记到 Cache
func (r *LocalRunner) watchSegments(job *Job, profile Profile, dir string, stop chan struct{}) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.scanAndPublish(job, profile, dir, false)
		}
	}
}

func (r *LocalRunner) scanAndPublish(job *Job, profile Profile, dir string, finalScan bool) {
	jc, err := r.mgr.Cache.getOrCreate(job.ID, profile.Name)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type segFile struct {
		seq  int
		path string
		size int64
	}
	var segs []segFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "seg-") || !strings.HasSuffix(name, ".ts") {
			continue
		}
		numStr := strings.TrimSuffix(strings.TrimPrefix(name, "seg-"), ".ts")
		seq, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		segs = append(segs, segFile{seq: seq, path: filepath.Join(dir, name), size: fi.Size()})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].seq < segs[j].seq })

	// 【优化】解析 ffmpeg 写出的 playlist.m3u8 获取每个切片的真实时长
	// 避免硬编码 segDur 导致 HLS.js 显示总时长不准确
	realDurations := parseFFmpegPlaylistDurations(filepath.Join(dir, "playlist.m3u8"))

	// ffmpeg 在写入 .ts 时是边写边写,稳妥起见仅登记除最后一片以外的切片(最后一片可能未写完)
	limit := len(segs)
	if !finalScan && limit > 0 {
		limit--
	}
	defaultDur := float64(setting.GetInt(conf.TranscodeSegmentDuration, 6))
	for i := 0; i < limit; i++ {
		s := segs[i]
		jc.mu.RLock()
		_, exists := jc.segments[s.seq]
		jc.mu.RUnlock()
		if exists {
			continue
		}
		// 优先用 ffmpeg playlist 中的真实时长
		dur := defaultDur
		if d, ok := realDurations[s.seq]; ok && d > 0 {
			dur = d
		}
		// 直接登记，不复制（已经在 cache 目录里了）
		jc.mu.Lock()
		jc.segments[s.seq] = &SegmentInfo{Seq: s.seq, Duration: dur, Size: s.size, Path: s.path}
		var toClose []chan struct{}
		for sk, chs := range jc.waiters {
			if sk <= s.seq {
				toClose = append(toClose, chs...)
				delete(jc.waiters, sk)
			}
		}
		jc.mu.Unlock()
		for _, ch := range toClose {
			close(ch)
		}
		// 第一片就绪 → 通知 job ready
		if s.seq == 0 {
			job.MarkReady()
		}
	}
	if finalScan {
		jc.mu.Lock()
		jc.final = true
		jc.mu.Unlock()
	}
}

// parseFFmpegPlaylistDurations 解析 ffmpeg 写出的 m3u8，返回 seq -> duration 的映射
// 用于获取每个切片的真实时长，避免后端用硬编码 segDur 导致总时长显示偏差
func parseFFmpegPlaylistDurations(path string) map[int]float64 {
	out := make(map[int]float64)
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	lines := strings.Split(string(data), "\n")
	var pendingDur float64
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXTINF:") {
			// #EXTINF:6.000000,
			body := strings.TrimPrefix(line, "#EXTINF:")
			body = strings.TrimSuffix(body, ",")
			d, err := strconv.ParseFloat(strings.TrimSpace(body), 64)
			if err == nil {
				pendingDur = d
			}
			continue
		}
		if strings.HasSuffix(line, ".ts") && strings.HasPrefix(line, "seg-") {
			numStr := strings.TrimSuffix(strings.TrimPrefix(line, "seg-"), ".ts")
			if seq, err := strconv.Atoi(numStr); err == nil && pendingDur > 0 {
				out[seq] = pendingDur
			}
			pendingDur = 0
		}
	}
	return out
}

// buildFFmpegArgs 构造 ffmpeg 命令行
func buildFFmpegArgs(job *Job, profile Profile, segPattern, playlistPath string) []string {
	hw := strings.ToLower(profile.HWAccel)
	args := []string{"-y", "-loglevel", "warning"}

	// 输入侧硬件加速
	switch hw {
	case "nvenc":
		args = append(args, "-hwaccel", "cuda")
	case "qsv":
		args = append(args, "-hwaccel", "qsv")
	case "vaapi":
		args = append(args, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi", "-vaapi_device", "/dev/dri/renderD128")
	case "amf":
		args = append(args, "-hwaccel", "d3d11va")
	case "videotoolbox":
		args = append(args, "-hwaccel", "videotoolbox")
	case "auto":
		args = append(args, "-hwaccel", "auto")
	}

	// 输入
	args = append(args, "-i", job.SourceURL)

	// 视频编码器
	vcodec := pickEncoder(profile.VideoCodec, hw)
	args = append(args, "-c:v", vcodec)
	// 【关键】强制浏览器兼容的 profile/level/pix_fmt，必须与 master.m3u8 声明的
	// CODECS="avc1.640028" (H.264 High Profile Level 4.0) 一致，否则 HLS.js 会触发
	// bufferAppendError 死循环。
	if strings.Contains(vcodec, "264") {
		args = append(args, "-profile:v", "high", "-level:v", "4.0", "-pix_fmt", "yuv420p")
	}
	if profile.VideoBitrate != "" {
		args = append(args, "-b:v", profile.VideoBitrate)
		args = append(args, "-maxrate", profile.VideoBitrate, "-bufsize", profile.VideoBitrate)
	}
	if profile.Scale != "" {
		// 软件 / 硬件缩放
		switch hw {
		case "vaapi":
			args = append(args, "-vf", "scale_vaapi="+profile.Scale)
		case "qsv":
			args = append(args, "-vf", "scale_qsv="+profile.Scale)
		default:
			args = append(args, "-vf", "scale="+profile.Scale)
		}
	}
	args = append(args, "-preset", defaultPresetFor(vcodec))

	// 音频
	// 【关键修复】不能使用 -c:a copy，因为浏览器 MSE 不支持 AC3/DTS/EAC3/TrueHD 等
	// 常见于 mkv 的音频编码。直接 copy 会导致 SourceBuffer.appendBuffer 失败
	// 触发 bufferAppendError 死循环。统一强制转 AAC 兼容所有浏览器。
	ac := profile.AudioCodec
	if ac == "" || ac == "copy" {
		ac = "aac"
	}
	args = append(args, "-c:a", ac)
	ab := profile.AudioBitrate
	if ab == "" {
		ab = "160k"
	}
	args = append(args, "-b:a", ab)
	// 强制立体声 + 48kHz，避免 5.1 声道 / 高采样率导致的兼容问题
	args = append(args, "-ac", "2", "-ar", "48000")

	// HLS 输出
	segDur := setting.GetInt(conf.TranscodeSegmentDuration, 6)
	// 【关键】强制关键帧对齐切片边界，确保每个切片以 IDR 帧开始可独立解码。
	// 没有这些参数时，第一个切片可能不含 IDR 帧，导致 HLS.js bufferAppendError。
	// fps 假设 25 (帧率未知时的保守值)，gop = segDur * fps；force_key_frames 让
	// ffmpeg 在每个切片边界强制插入关键帧。
	gop := segDur * 25
	args = append(args,
		"-g", strconv.Itoa(gop),
		"-keyint_min", strconv.Itoa(gop),
		"-sc_threshold", "0",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", segDur),
	)
	args = append(args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(segDur),
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", segPattern,
		"-hls_flags", "independent_segments+temp_file",
		playlistPath,
	)
	return args
}

// pickEncoder 根据视频编码 + HW 类型选择 ffmpeg 编码器
func pickEncoder(codec, hw string) string {
	codec = strings.ToLower(codec)
	switch codec {
	case "h264", "":
		switch hw {
		case "nvenc":
			return "h264_nvenc"
		case "qsv":
			return "h264_qsv"
		case "vaapi":
			return "h264_vaapi"
		case "amf":
			return "h264_amf"
		case "videotoolbox":
			return "h264_videotoolbox"
		default:
			return "libx264"
		}
	case "hevc", "h265":
		switch hw {
		case "nvenc":
			return "hevc_nvenc"
		case "qsv":
			return "hevc_qsv"
		case "vaapi":
			return "hevc_vaapi"
		case "amf":
			return "hevc_amf"
		case "videotoolbox":
			return "hevc_videotoolbox"
		default:
			return "libx265"
		}
	case "av1":
		switch hw {
		case "nvenc":
			return "av1_nvenc"
		case "qsv":
			return "av1_qsv"
		default:
			return "libsvtav1"
		}
	}
	return "libx264"
}

func defaultPresetFor(encoder string) string {
	if strings.Contains(encoder, "nvenc") {
		return "p5"
	}
	if strings.Contains(encoder, "qsv") {
		return "medium"
	}
	if strings.Contains(encoder, "amf") {
		return "balanced"
	}
	if strings.Contains(encoder, "videotoolbox") {
		return "medium"
	}
	return "veryfast"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// ProbeSource 调用 ffprobe 探测源文件
func ProbeSource(url string) (SourceProbe, error) {
	probePath := setting.GetStr(conf.TranscodeFFprobePath, "ffprobe")
	args := []string{
		"-v", "error",
		"-show_entries", "format=duration,size,bit_rate:stream=codec_type,codec_name,bit_rate,width,height",
		"-of", "json",
		url,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, probePath, args...)
	if runtime.GOOS == "windows" {
		// no special handling
	}
	out, err := cmd.Output()
	if err != nil {
		return SourceProbe{}, err
	}
	var raw struct {
		Format struct {
			Duration string `json:"duration"`
			Size     string `json:"size"`
			BitRate  string `json:"bit_rate"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			BitRate   string `json:"bit_rate"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return SourceProbe{}, err
	}
	p := SourceProbe{}
	p.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	p.Size, _ = strconv.ParseInt(raw.Format.Size, 10, 64)
	for _, s := range raw.Streams {
		if s.CodecType == "video" && p.VideoCodec == "" {
			p.VideoCodec = s.CodecName
			p.Width = s.Width
			p.Height = s.Height
			p.VideoBitrate, _ = strconv.ParseInt(s.BitRate, 10, 64)
		}
		if s.CodecType == "audio" && p.AudioCodec == "" {
			p.AudioCodec = s.CodecName
			p.AudioBitrate, _ = strconv.ParseInt(s.BitRate, 10, 64)
		}
	}
	return p, nil
}
