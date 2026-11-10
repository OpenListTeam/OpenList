package meeting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type mediaInfo struct {
	Duration     float64
	Video, Audio bool
}

// ffprobe 只读取媒体元信息，远程地址必须是本进程受控的回环中转地址。
// 不等待播放完成，也不使用输出断流来推断整场会议已经下载完整。
func probe(ctx context.Context, source string, remote bool) (mediaInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	protocols := "file"
	if remote {
		protocols = "http,tcp,crypto"
	}
	cmd := exec.CommandContext(ctx, executable("FFPROBE", "ffprobe"), "-v", "error", "-protocol_whitelist", protocols, "-rw_timeout", "15000000", "-show_entries", "format=format_name,duration:stream=codec_type", "-of", "json", source)
	var out limitedBuffer
	cmd.Stdout = &out
	if cmd.Run() != nil {
		if ctx.Err() != nil {
			return mediaInfo{}, ctx.Err()
		}
		return mediaInfo{}, fmt.Errorf("无法读取完整视频信息，请检查访问权限、Cookie 或媒体格式")
	}
	var value struct {
		Format struct {
			Name     string `json:"format_name"`
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Type string `json:"codec_type"`
		} `json:"streams"`
	}
	if json.Unmarshal(out.Bytes(), &value) != nil || (!strings.Contains(value.Format.Name, "mp4") && !(remote && strings.Contains(value.Format.Name, "hls"))) {
		return mediaInfo{}, fmt.Errorf("仅支持完整 MP4 或已结束的 HLS 视频")
	}
	duration, err := strconv.ParseFloat(value.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return mediaInfo{}, fmt.Errorf("无法确认会议视频完整时长")
	}
	result := mediaInfo{Duration: duration}
	for _, s := range value.Streams {
		result.Video = result.Video || s.Type == "video"
		result.Audio = result.Audio || s.Type == "audio"
	}
	if !result.Video {
		return mediaInfo{}, fmt.Errorf("媒体中没有视频轨道")
	}
	return result, nil
}
func sameMedia(a, b mediaInfo) bool {
	return a.Video == b.Video && a.Audio == b.Audio && math.Abs(a.Duration-b.Duration) <= math.Min(2, math.Max(.5, b.Duration*.001))
}

// contextReader 为摘要和本地复制提供取消检查，文件很大时也不会阻塞取消。
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.reader.Read(p)
}
func digest(ctx context.Context, p string) (string, int64, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, contextReader{ctx: ctx, reader: f})
	return hex.EncodeToString(h.Sum(nil)), n, e
}
func atomicFile(p string, data []byte) error {
	f, e := os.CreateTemp(filepath.Dir(p), ".state-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, e = f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, p)
}
func publish(ctx context.Context, src, dst string) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(dst+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, contextReader{ctx: ctx, reader: in})
	if e == nil {
		e = out.Sync()
	}
	closeErr := out.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(dst+".part", dst)
}

type cacheRecord struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func validCache(ctx context.Context, work, final string) bool {
	raw, e := os.ReadFile(filepath.Join(work, "complete.json"))
	if e != nil {
		return false
	}
	var saved cacheRecord
	if json.Unmarshal(raw, &saved) != nil || saved.Size <= 0 || saved.SHA256 == "" {
		return false
	}
	sum, size, e := digest(ctx, final)
	return e == nil && size == saved.Size && sum == saved.SHA256
}
