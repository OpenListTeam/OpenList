package common

import (
	"context"
	"fmt"
	"net"
	"net/http"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

func GetApiUrlFromRequest(r *http.Request) string {
	api := conf.Conf.SiteURL
	if strings.HasPrefix(api, "http") {
		return strings.TrimSuffix(api, "/")
	}
	if r != nil {
		protocol := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			protocol = "https"
		}
		host := r.Header.Get("X-Forwarded-Host")
		if host == "" {
			host = r.Host
		}
		api = fmt.Sprintf("%s://%s", protocol, stdpath.Join(host, api))
	}
	api = strings.TrimSuffix(api, "/")
	return api
}

func GetApiUrl(ctx context.Context) string {
	return conf.GetApiUrl(ctx)
}

// GetPlaybackBase 返回要拼进「返回给前端的播放地址」的站点前缀。
//
// 与 GetApiUrlFromRequest 的区别只在一种场景：站点没有显式配置 SiteURL，
// 且请求 Host 是回环地址（127.0.0.1 / localhost / ::1）。这时返回空串，
// 让调用方生成相对路径（/d/... 、/p/...），由浏览器按当前页面的 origin 解析。
// 否则播放器会拿到 http://127.0.0.1:5244/...，在反向代理、容器映射端口或
// 其它设备访问时都会指向错误的主机。
func GetPlaybackBase(r *http.Request) string {
	// 只要配置了 site_url 就走绝对地址。site_url 除了完整 URL，也可能是
	// "/openlist" 这种路径前缀（GetApiUrlFromRequest 会把它拼到 host 后面）；
	// 此时若按「回环地址」返回相对路径，前缀会丢掉，播放地址直接 404。
	if strings.TrimSpace(conf.Conf.SiteURL) != "" {
		return GetApiUrlFromRequest(r)
	}
	if r != nil && isLoopbackHost(playbackHost(r)) {
		return ""
	}
	return GetApiUrlFromRequest(r)
}

// playbackHost 取实际参与地址拼接的 host（优先反代头，其次 Request.Host）。
func playbackHost(r *http.Request) string {
	if host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); host != "" {
		return host
	}
	return r.Host
}

// isLoopbackHost 判断 host（可带端口）是否指向本机回环地址。
func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	if parsed, _, err := net.SplitHostPort(h); err == nil {
		h = parsed
	}
	h = strings.ToLower(strings.Trim(strings.TrimSuffix(h, "."), "[]"))
	switch h {
	case "localhost", "localhost.localdomain", "0.0.0.0":
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}
