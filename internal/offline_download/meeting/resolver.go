package meeting

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed resolver.mjs
var resolverScript []byte

type mediaSource struct {
	URL     string            `json:"url"`
	Kind    string            `json:"kind"`
	Headers map[string]string `json:"headers"`
}

// resolve 与 yt-dlp 一样先按 URL 选择提取器。直链无需浏览器；平台网页
// 使用独立临时会话观察播放器请求，空 Cookie 时不注入任何登录凭据。
func resolve(ctx context.Context, raw, cookie, work string) (mediaSource, error) {
	if platform(raw) == "" {
		kind := "mp4"
		u, _ := mediaURL(raw)
		if strings.HasSuffix(strings.ToLower(u.Path), ".m3u8") {
			kind = "hls"
		}
		headers := map[string]string{}
		if cookie != "" {
			headers["Cookie"] = cookie
		}
		return mediaSource{URL: raw, Kind: kind, Headers: headers}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	p := filepath.Join(work, "resolver.mjs")
	if err := os.WriteFile(p, resolverScript, 0600); err != nil {
		return mediaSource{}, err
	}
	payload, _ := json.Marshal(map[string]string{"url": raw, "cookie": cookie, "browser_path": executable("BROWSER", "/usr/bin/chromium-browser"), "module_dir": executable("MODULE_DIR", "/opt/openlist/browser")})
	cmd := exec.CommandContext(ctx, executable("NODE", "node"), p)
	cmd.Stdin = bytes.NewReader(payload)
	var out limitedBuffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return mediaSource{}, ctx.Err()
	}
	var reply struct {
		mediaSource
		Error string `json:"error"`
	}
	if json.Unmarshal(out.Bytes(), &reply) != nil {
		return mediaSource{}, fmt.Errorf("会议解析器无法启动，请检查 Node 和浏览器依赖")
	}
	if reply.Error == "NO_MEDIA_OR_LOGIN_REQUIRED" {
		if cookie == "" {
			return mediaSource{}, fmt.Errorf("匿名解析失败：未找到可下载视频，链接可能需要登录，可填写有效 Cookie 后重试")
		}
		return mediaSource{}, fmt.Errorf("未找到可下载视频，请检查链接权限或 Cookie 是否过期")
	}
	if err != nil || reply.Error != "" || reply.URL == "" {
		return mediaSource{}, fmt.Errorf("会议链接解析失败，请检查链接、访问权限及浏览器依赖")
	}
	if reply.Kind != "mp4" && reply.Kind != "hls" {
		return mediaSource{}, fmt.Errorf("会议视频格式暂不支持")
	}
	if _, err := mediaURL(reply.URL); err != nil {
		return mediaSource{}, err
	}
	return reply.mediaSource, nil
}

// 外部进程输出有明确上限，原始异常和授权请求头不写入任务日志。
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, fmt.Errorf("process output exceeds limit")
	}
	return b.Buffer.Write(p)
}
