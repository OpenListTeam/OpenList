package handles

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	stdpath "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
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
	host = strings.ToLower(host)
	for _, h := range coverAllowedHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// PublicMediaProxy 代理外部封面图（TMDB/豆瓣/Discogs），解决客户端直连被墙的问题。
// 用法：/api/public/media/proxy?url=https%3A%2F%2Fimage.tmdb.org%2F...
func PublicMediaProxy(c *gin.Context) {
	raw := c.Query("url")
	if raw == "" {
		c.Status(http.StatusBadRequest)
		return
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !hostAllowed(u.Hostname()) {
		c.Status(http.StatusForbidden)
		return
	}
	resp, err := fetchCover(c.Request.Context(), u.String())
	if err != nil {
		// 直连失败时回退到公共图片网关（部分网络环境下 TMDB 图片域名不可达）
		gateway := "https://wsrv.nl/?url=" + url.QueryEscape(strings.TrimPrefix(strings.TrimPrefix(u.String(), "https://"), "http://"))
		resp, err = fetchCover(c.Request.Context(), gateway)
	}
	if err != nil {
		utils.Log.Warnf("media cover proxy failed: %s: %v", u.String(), err)
		c.Status(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	if !strings.HasPrefix(ct, "image/") {
		c.Status(http.StatusUnsupportedMediaType)
		return
	}
	c.Header("Content-Type", ct)
	c.Header("Cache-Control", "public, max-age=604800")
	_, _ = io.Copy(c.Writer, io.LimitReader(resp.Body, 8<<20))
}

// fetchCover 拉取图片，仅接受 200 且 Content-Type 为 image/*
func fetchCover(ctx context.Context, raw string) (*http.Response, error) {
	client := &http.Client{Timeout: 20 * time.Second}
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
	reqPath := strings.TrimPrefix(stdpath.Clean("/"+c.Query("path")), "/")
	if reqPath == "" || reqPath == "." || strings.Contains(reqPath, "..") {
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
	if err != nil || strings.HasPrefix(rel, "..") {
		c.Status(http.StatusForbidden)
		return
	}
	ct := mime.TypeByExtension(filepath.Ext(full))
	if ct == "" {
		ct = "image/jpeg"
	}
	c.Header("Content-Type", ct)
	c.Header("Cache-Control", "public, max-age=604800")
	c.File(full)
}
