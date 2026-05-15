// Package transcode 实现 OpenList 的 FFmpeg 云端/本地转码功能。
// 设计目标：
//  1. 总开关默认关，开启后按文件大小、源码率、源编码、文件后缀过滤决定是否转码。
//  2. 协议自研，纯 HTTP/JSON，可挂接本地内置 Worker 或远程 Worker 节点池。
//  3. 切片流式回吐，支持边转边播。
package transcode

import (
	"sync"
	"time"
)

// JobStatus 转码任务状态
type JobStatus string

const (
	JobPending  JobStatus = "pending"  // 等待 Worker 领取
	JobRunning  JobStatus = "running"  // Worker 已领取并在转码
	JobReady    JobStatus = "ready"    // 至少首切片可用
	JobFinished JobStatus = "finished" // 全部切片完成
	JobFailed   JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// OutputFormat 输出封装格式
type OutputFormat string

const (
	FormatHLS  OutputFormat = "hls"
	FormatDASH OutputFormat = "dash"
	FormatMP4  OutputFormat = "mp4"
)

// HWAccel 硬件加速类型
type HWAccel string

const (
	HWNone         HWAccel = "none"
	HWAuto         HWAccel = "auto"
	HWNVENC        HWAccel = "nvenc"        // NVIDIA
	HWQSV          HWAccel = "qsv"          // Intel QuickSync
	HWVAAPI        HWAccel = "vaapi"        // Linux VA-API
	HWAMF          HWAccel = "amf"          // AMD AMF
	HWVideoToolbox HWAccel = "videotoolbox" // macOS
)

// SourceProbe 源文件探测信息（由 ffprobe 得出）
type SourceProbe struct {
	Duration     float64 `json:"duration"`      // 秒
	Size         int64   `json:"size"`          // 字节
	VideoCodec   string  `json:"video_codec"`   // h264 / hevc / av1 / vp9 ...
	VideoBitrate int64   `json:"video_bitrate"` // bps
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	AudioCodec   string  `json:"audio_codec"`
	AudioBitrate int64   `json:"audio_bitrate"`
}

// Profile 输出转码档位
type Profile struct {
	Name         string `json:"name"`           // 例如 1080p
	VideoCodec   string `json:"video_codec"`    // h264 / hevc / av1
	VideoBitrate string `json:"video_bitrate"`  // 例如 4000k
	Scale        string `json:"scale"`          // 例如 1920:-2
	AudioCodec   string `json:"audio_codec"`    // aac / mp3 / opus / copy
	AudioBitrate string `json:"audio_bitrate"`  // 例如 160k
	HWAccel      string `json:"hwaccel"`        // worker 端如何选择硬件加速
}

// OutputSpec 输出参数
type OutputSpec struct {
	Format          OutputFormat `json:"format"`
	SegmentDuration int          `json:"segment_duration"` // 秒
}

// Job 一次转码任务
type Job struct {
	ID            string       `json:"id"`
	Path          string       `json:"path"`           // OpenList 文件路径
	SourceURL     string       `json:"source_url"`     // 提供给 Worker 的签名 URL
	Probe         SourceProbe  `json:"probe"`
	Profiles      []Profile    `json:"profiles"`
	Output        OutputSpec   `json:"output"`
	CallbackToken string       `json:"callback_token"` // Worker 回吐切片用
	Deadline      int64        `json:"deadline"`       // unix 秒

	// 运行时
	Status       JobStatus `json:"status"`
	WorkerID     string    `json:"worker_id"`
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	LastAccessAt time.Time `json:"last_access_at"` // 播放端最后一次请求 segment/playlist 的时间
	Error        string    `json:"error,omitempty"`

	// 内部：用于"边转边播"——首切片就绪后关闭该 channel
	readyOnce sync.Once
	readyCh   chan struct{}
	mu        sync.RWMutex
}

// NewJob 构造任务（自动初始化内部 channel）
func NewJob() *Job {
	now := time.Now()
	return &Job{
		Status:       JobPending,
		CreatedAt:    now,
		LastAccessAt: now,
		readyCh:      make(chan struct{}),
	}
}

// Touch 更新最后访问时间（播放端每次请求 segment/playlist 时调用）
func (j *Job) Touch() {
	j.mu.Lock()
	j.LastAccessAt = time.Now()
	j.mu.Unlock()
}

// GetLastAccess 获取最后访问时间（线程安全）
func (j *Job) GetLastAccess() time.Time {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.LastAccessAt
}

// GetStatus 获取当前状态（线程安全）
func (j *Job) GetStatus() JobStatus {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Status
}

// MarkReady 标记任务已可播放（首切片就绪）
func (j *Job) MarkReady() {
	j.readyOnce.Do(func() {
		j.mu.Lock()
		if j.Status == JobRunning || j.Status == JobPending {
			j.Status = JobReady
		}
		j.mu.Unlock()
		close(j.readyCh)
	})
}

// WaitReady 阻塞等待首切片就绪
func (j *Job) WaitReady() <-chan struct{} { return j.readyCh }

// Worker 节点信息
type Worker struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Version       string    `json:"version"`
	Capacity      int       `json:"capacity"`        // 最大并发任务数
	HWAccel       []string  `json:"hwaccel"`         // 支持的硬件加速
	CodecsDecode  []string  `json:"codecs_decode"`   // 可解码
	CodecsEncode  []string  `json:"codecs_encode"`   // 可编码
	MaxResolution string    `json:"max_resolution"`  // 最大分辨率
	Tags          []string  `json:"tags"`
	Endpoint      string    `json:"endpoint,omitempty"` // 反向连接地址（可选）

	// 运行时
	Load        float64   `json:"load"`         // 0~1
	Running     []string  `json:"running"`      // 当前 job_ids
	FreeSlots   int       `json:"free_slots"`
	LastSeen    time.Time `json:"last_seen"`
	RegisteredAt time.Time `json:"registered_at"`
	Local       bool      `json:"local"` // 是否本地内置 Worker
}
