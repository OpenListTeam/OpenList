// Package meeting 实现独立会议视频下载，不连接或调用任何流媒体服务器。
package meeting

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/google/uuid"
)

const Name = "会议视频下载工具"

// Tool 沿用同步 Run → Transfer 生命周期，下载器不直接操作任何存储驱动。
type Tool struct{ ready atomic.Bool }

var Default = &Tool{}

func init()                              { tool.Tools.Add(Default) }
func (*Tool) Name() string               { return Name }
func (*Tool) Items() []model.SettingItem { return nil }
func (m *Tool) IsReady() bool            { return m.ready.Load() }

// 依赖路径由部署环境设置，普通用户无法通过下载请求替换可执行文件。
func executable(key, fallback string) string {
	if value := os.Getenv("OPENLIST_MEETING_" + key); value != "" {
		return value
	}
	return fallback
}

// Init 只检测独立工具需要的依赖。
func (m *Tool) Init() (string, error) {
	m.ready.Store(false)
	for _, p := range []string{executable("NODE", "node"), executable("BROWSER", "/usr/bin/chromium-browser"), executable("PYTHON", "/opt/openlist/meeting/bin/python3"), executable("FFMPEG", "ffmpeg"), executable("FFPROBE", "ffprobe")} {
		if _, err := exec.LookPath(p); err != nil {
			return "", fmt.Errorf("会议视频下载工具缺少依赖，请安装配套镜像或检查运行路径")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable("PYTHON", "/opt/openlist/meeting/bin/python3"), "-c", "import yt_dlp")
	if cmd.Run() != nil {
		return "", fmt.Errorf("会议视频下载工具缺少 yt-dlp 下载引擎")
	}
	m.ready.Store(true)
	return "会议视频下载工具已就绪", nil
}
func (*Tool) AddURL(*tool.AddUrlArgs) (string, error)         { return "", errs.NotSupport }
func (*Tool) Remove(*tool.DownloadTask) error                 { return errs.NotSupport }
func (*Tool) Status(*tool.DownloadTask) (*tool.Status, error) { return nil, errs.NotSupport }

// PrepareTask 在任务进入已有队列前校验输入并加密 Cookie；没有 Cookie 合法，
// 后续会真实尝试匿名解析，只有解析/下载失败才报告错误。
func (*Tool) PrepareTask(t *tool.DownloadTask) error {
	if _, err := uuid.Parse(t.ID); err != nil {
		return fmt.Errorf("会议任务标识无效")
	}
	if err := ValidateBatch([]string{t.Url}, t.SourceCookie); err != nil {
		return err
	}
	if t.DeletePolicy != tool.DeleteNever && t.DeletePolicy != tool.DeleteOnUploadSucceed {
		return fmt.Errorf("会议视频下载仅支持保留缓存或上传成功后删除")
	}
	if t.SourceCookie != "" {
		sealed, err := sealCookie(t.ID, t.SourceCookie)
		if err != nil {
			return err
		}
		t.MeetingCookie = sealed
	}
	t.SourceCookie = ""
	return nil
}

// Run 的中间文件和凭据工作目录不放入 TempDir，只有校验完成的最终 MP4
// 才进入转存目录，防止 .part、清单或内部状态被作为文件上传到用户网盘。
func (*Tool) Run(t *tool.DownloadTask) error {
	if _, err := uuid.Parse(t.ID); err != nil {
		return fmt.Errorf("会议任务标识无效")
	}
	if t.DeletePolicy != tool.DeleteNever && t.DeletePolicy != tool.DeleteOnUploadSucceed {
		return fmt.Errorf("会议视频下载仅支持保留缓存或上传成功后删除")
	}
	if err := ValidateBatch([]string{t.Url}, ""); err != nil {
		return err
	}
	ctx := t.Ctx()
	work := filepath.Join(conf.DataDir, "meeting-video", "tasks", t.ID)
	if err := os.MkdirAll(work, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(t.TempDir, 0700); err != nil {
		return err
	}
	final := filepath.Join(t.TempDir, "meeting-"+t.ID+".mp4")
	// 缓存仅在大小与摘要均匹配时复用，转存失败后的下载重试不必重新获取。
	if validCache(ctx, work, final) {
		t.SetProgress(100)
		return nil
	}
	cookie, err := unsealCookie(t.ID, t.MeetingCookie)
	if err != nil {
		return err
	}
	t.Status = "[会议视频下载工具] 解析视频链接"
	source, err := resolve(ctx, t.Url, cookie, work)
	if err != nil {
		return err
	}
	relay, err := newRelay(ctx, source, nil)
	if err != nil {
		return err
	}
	defer relay.Close()
	expected, err := probe(ctx, relay.URL, true)
	if err != nil {
		return err
	}
	t.Status = "[会议视频下载工具] 直接下载视频"
	output, err := download(ctx, t, relay.URL, work)
	if err != nil {
		return err
	}
	t.Status = "[会议视频下载工具] 校验完整视频"
	actual, err := probe(ctx, output, false)
	if err != nil {
		return err
	}
	if !sameMedia(actual, expected) {
		return fmt.Errorf("会议视频音视频轨道或时长不完整，未提交转存")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// 工作目录与缓存目录可能处于不同卷，因此使用带取消的流复制而非跨卷 rename。
	if err := publish(ctx, output, final); err != nil {
		return err
	}
	sum, size, err := digest(ctx, final)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(cacheRecord{SHA256: sum, Size: size})
	if err := atomicFile(filepath.Join(work, "complete.json"), raw); err != nil {
		return err
	}
	// 最终文件已持久化，移除工作副本，避免上传成功后仍残留另一份完整视频。
	_ = os.Remove(output)
	t.SetTotalBytes(size)
	t.SetProgress(100)
	t.Status = "[会议视频下载工具] 下载完成，交由离线转存"
	return nil
}
