package meeting

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
)

//go:embed downloader.py
var downloaderScript []byte

// download 通过 stdin 传递任务输入；进度只接受数值，不透传第三方错误。
// yt-dlp 对点播直接读取分片，不使用 -re，也不进行音视频重新编码。
func download(ctx context.Context, t *tool.DownloadTask, url, work string) (string, error) {
	script := filepath.Join(work, "downloader.py")
	if err := os.WriteFile(script, downloaderScript, 0600); err != nil {
		return "", err
	}
	// yt-dlp 的 ffmpeg_location 接收文件/目录路径，不能直接传 PATH 中的命令名。
	ffmpeg, err := exec.LookPath(executable("FFMPEG", "ffmpeg"))
	if err != nil {
		return "", fmt.Errorf("未找到 FFmpeg 封装工具")
	}
	payload, _ := json.Marshal(map[string]string{"url": url, "work": work, "ffmpeg": ffmpeg})
	cmd := exec.CommandContext(ctx, executable("PYTHON", "/opt/openlist/meeting/bin/python3"), script)
	configureDownloadProcess(cmd)
	cmd.Stdin = bytes.NewReader(payload)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err = cmd.Start(); err != nil {
		return "", fmt.Errorf("会议下载引擎无法启动，请检查 Python 与 yt-dlp")
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 128<<10)
	done := false
	for scanner.Scan() {
		var event struct {
			Bytes int64 `json:"bytes"`
			Total int64 `json:"total"`
			Done  bool  `json:"done"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Total > 0 && event.Bytes >= 0 {
			t.SetTotalBytes(event.Total)
			p := float64(event.Bytes) * 95 / float64(event.Total)
			if p > 95 {
				p = 95
			}
			t.SetProgress(p)
		}
		done = done || event.Done
	}
	if scanner.Err() != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if waitErr != nil || scanner.Err() != nil || !done {
		return "", fmt.Errorf("会议视频下载失败：请检查链接有效期、Cookie、下载权限或网络；未提交转存")
	}
	return filepath.Join(work, "media.mp4"), nil
}
