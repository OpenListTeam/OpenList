package meeting

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
)

// 此测试需配套容器内的真实 yt-dlp/FFmpeg。生成两分钟小视频，验证下载
// 不按播放时长运行；夹具覆盖匿名、Cookie、完整 HLS 和缺失分片失败。
func TestRealDownloadEngine(t *testing.T) {
	if os.Getenv("MEETING_INTEGRATION") != "1" {
		t.Skip("requires media dependencies")
	}
	root := t.TempDir()
	source := filepath.Join(root, "video.mp4")
	cmd := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=160x90:rate=10", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "120", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-g", "20", "-c:a", "aac", "-movflags", "+faststart", source)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate media: %v %s", err, data)
	}
	cmd = exec.Command("ffmpeg", "-v", "error", "-y", "-i", source, "-c", "copy", "-f", "hls", "-hls_time", "2", "-hls_list_size", "0", filepath.Join(root, "video.m3u8"))
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate HLS: %v %s", err, data)
	}
	files := http.FileServer(http.Dir(root))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/private/") {
			if r.Header.Get("Cookie") != "session=fixture" {
				w.WriteHeader(401)
				return
			}
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/private")
		}
		if r.URL.Path == "/broken.m3u8" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:120\n#EXTINF:120,\n/missing.ts\n#EXT-X-ENDLIST\n")
			return
		}
		files.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	for _, tc := range []struct {
		name, file, kind, cookie string
		fail                     bool
	}{
		{"anonymous-mp4", "/video.mp4", "mp4", "", false},
		{"authenticated-mp4", "/private/video.mp4", "mp4", "session=fixture", false},
		{"anonymous-forbidden", "/private/video.mp4", "mp4", "", true},
		{"finite-hls", "/video.m3u8", "hls", "", false},
		{"missing-hls-segment", "/broken.m3u8", "hls", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			defer cancel()
			relay, err := newRelay(ctx, mediaSource{URL: upstream.URL + tc.file, Kind: tc.kind, Headers: map[string]string{"Cookie": tc.cookie}}, http.DefaultTransport)
			if err != nil {
				t.Fatal(err)
			}
			defer relay.Close()
			task := &tool.DownloadTask{}
			started := time.Now()
			output, err := download(ctx, task, relay.URL, t.TempDir())
			if tc.fail {
				if err == nil {
					t.Fatal("reported success for unavailable media")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := probe(ctx, output, false)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Video || !got.Audio || got.Duration < 119 || got.Duration > 121 {
				t.Fatalf("incomplete output %+v", got)
			}
			t.Logf("120-second video downloaded in %s", time.Since(started).Round(time.Millisecond))
			if time.Since(started) > 45*time.Second {
				t.Fatal("download unexpectedly followed playback duration")
			}
		})
	}
}
