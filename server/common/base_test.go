package common

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":         true,
		"127.0.0.1:5244":    true,
		"localhost":         true,
		"LocalHost:5244":    true,
		"[::1]:5244":        true,
		"::1":               true,
		"0.0.0.0:5244":      true,
		"":                  false,
		"192.168.1.10:5244": false,
		"example.com":       false,
		"127.0.0.1.evil.io": false,
	}
	for host, want := range cases {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestGetPlaybackBase(t *testing.T) {
	old := conf.Conf
	defer func() { conf.Conf = old }()
	conf.Conf = conf.DefaultConfig("data")

	newReq := func(host, fwdHost string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/media/stream_url", nil)
		if fwdHost != "" {
			r.Header.Set("X-Forwarded-Host", fwdHost)
		}
		return r
	}

	conf.Conf.SiteURL = ""
	if got := GetPlaybackBase(newReq("127.0.0.1:5244", "")); got != "" {
		t.Errorf("loopback host: got %q, want relative (empty)", got)
	}
	if got := GetPlaybackBase(newReq("localhost:5244", "")); got != "" {
		t.Errorf("localhost: got %q, want relative (empty)", got)
	}
	// 反代给出了真实对外域名时，沿用绝对地址
	if got := GetPlaybackBase(newReq("127.0.0.1:5244", "media.example.com")); got != "http://media.example.com" {
		t.Errorf("forwarded host: got %q", got)
	}
	if got := GetPlaybackBase(newReq("192.168.1.10:5244", "")); got != "http://192.168.1.10:5244" {
		t.Errorf("lan host: got %q", got)
	}
	// 显式配置了 SiteURL 就以它为准，即使请求来自回环地址
	conf.Conf.SiteURL = "https://site.example.com/"
	if got := GetPlaybackBase(newReq("127.0.0.1:5244", "")); got != "https://site.example.com" {
		t.Errorf("site_url: got %q", got)
	}
	// site_url 写成子路径时也属于「已配置」：不能退化成相对路径，否则前缀会丢
	conf.Conf.SiteURL = "/openlist"
	if got := GetPlaybackBase(newReq("127.0.0.1:5244", "")); got != "http://127.0.0.1:5244/openlist" {
		t.Errorf("site_url path prefix: got %q", got)
	}
}
