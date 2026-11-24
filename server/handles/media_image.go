package handles

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	stdpath "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/media/imgmime"
	"github.com/OpenListTeam/OpenList/v4/internal/media/scraper"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/gin-gonic/gin"
)

// 允许代理的封面来源域名（TMDB / 豆瓣 / Discogs）。
// 仅代理图片，避免该接口被当作通用反向代理滥用。
var coverAllowedHosts = []string{
	"image.tmdb.org",
	"tmdb.org",
	"doubanio.com",
	"douban.com",
	"discogs.com",
}

func hostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".local") {
		return false
	}
	// 显式拒绝 IP 字面量，避免白名单域名被解析/重定向到内网
	if ip := net.ParseIP(host); ip != nil {
		return false
	}
	for _, h := range coverAllowedHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// safeCoverURL 校验封面地址的协议与域名（含重定向后的每一跳）
func safeCoverURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("empty url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme")
	}
	if !hostAllowed(u.Hostname()) {
		return fmt.Errorf("host not allowed")
	}
	return nil
}

// PublicMediaProxy 代理外部封面图（TMDB/豆瓣/Discogs），解决客户端直连被墙的问题。
// 用法：/api/public/media/proxy?url=https%3A%2F%2Fimage.tmdb.org%2F...
func PublicMediaProxy(c *gin.Context) {
	if !MediaEnabled() {
		c.Status(http.StatusNotFound)
		return
	}
	raw := c.Query("url")
	if raw == "" {
		c.Status(http.StatusBadRequest)
		return
	}
	u, err := url.Parse(raw)
	if err != nil || safeCoverURL(u) != nil {
		c.Status(http.StatusForbidden)
		return
	}
	resp, err := fetchCover(c.Request.Context(), u.String())
	if err != nil && setting.GetBool(conf.MediaCoverGateway) {
		// 直连失败时回退到公共图片网关（部分网络环境下 TMDB 图片域名不可达）。
		// 这会把封面 URL 交给第三方 wsrv.nl，因此默认关闭，需管理员显式开启。
		gateway := "https://wsrv.nl/?url=" + url.QueryEscape(strings.TrimPrefix(strings.TrimPrefix(u.String(), "https://"), "http://"))
		resp, err = fetchCover(c.Request.Context(), gateway)
	}
	if err != nil {
		utils.Log.Warnf("media cover proxy failed: %s: %v", u.String(), err)
		c.Status(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	// 不信任上游 Content-Type：读取有限字节后按魔数判定，只输出四种栅格图片，
	// 避免 image/svg+xml 等可执行脚本的类型以本站源被浏览器渲染。
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || len(data) == 0 {
		c.Status(http.StatusBadGateway)
		return
	}
	ct := imgmime.Sniff(data)
	if ct == "" {
		c.Status(http.StatusUnsupportedMediaType)
		return
	}
	setSafeImageHeaders(c)
	c.Header("Cache-Control", "public, max-age=604800")
	c.Data(http.StatusOK, ct, data)
}

// setSafeImageHeaders 为用户可影响内容的图片响应加上防 XSS 头。
func setSafeImageHeaders(c *gin.Context) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", `inline; filename="cover"`)
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
}

// fetchCover 拉取图片，仅接受 200 且 Content-Type 为 image/*
// 重定向的每一跳都重新做域名校验，避免白名单域名 302 到内网造成 SSRF。
func fetchCover(ctx context.Context, raw string) (*http.Response, error) {
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			return safeCoverURL(req.URL)
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OpenList-Media/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return resp, nil
}

// thumbnailRoot 返回运行目录模式下缩略图的本地根目录（<data>/<thumbnail_path>）
func thumbnailRoot() string {
	dir := setting.GetStr(conf.MediaThumbnailPath, "imgs")
	return scraper.RuntimeThumbRoot(dir)
}

// PublicMediaThumb 读取本地缩略图目录中的文件。
// 缩略图由刮削器写入本地磁盘（非 VFS），因此不能用 /d 读取。
// 用法：/api/public/media/thumb?path=/imgs/xxx.jpg
func PublicMediaThumb(c *gin.Context) {
	if !MediaEnabled() {
		c.Status(http.StatusNotFound)
		return
	}
	reqPath := strings.TrimPrefix(stdpath.Clean("/"+c.Query("path")), "/")
	// stdpath.Clean 已消解 ../ 段；这里不能再用 Contains("..") 粗暴过滤，会误杀 a..b.jpg 这类合法文件名。
	// 越界由下方 filepath.Rel 校验兜底。
	if reqPath == "" || reqPath == "." {
		c.Status(http.StatusBadRequest)
		return
	}
	base := strings.Trim(setting.GetStr(conf.MediaThumbnailPath, "/imgs"), "/")
	if base != "" {
		if !strings.HasPrefix(reqPath, base+"/") {
			c.Status(http.StatusNotFound)
			return
		}
		reqPath = strings.TrimPrefix(reqPath, base+"/")
	}
	root := thumbnailRoot()
	full := filepath.Join(root, filepath.FromSlash(reqPath))
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		c.Status(http.StatusForbidden)
		return
	}
	// 只按扩展名白名单输出栅格图片，拒绝 svg / html 等可执行类型
	var ct string
	switch strings.ToLower(filepath.Ext(full)) {
	case ".jpg", ".jpeg":
		ct = "image/jpeg"
	case ".png":
		ct = "image/png"
	case ".gif":
		ct = "image/gif"
	case ".webp":
		ct = "image/webp"
	default:
		c.Status(http.StatusUnsupportedMediaType)
		return
	}
	setSafeImageHeaders(c)
	c.Header("Content-Type", ct)
	c.Header("Cache-Control", "public, max-age=604800")
	c.File(full)
}
