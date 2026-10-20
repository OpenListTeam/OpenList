package net

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

func TestNewOSSClientUsesEnvironmentHTTPSProxy(t *testing.T) {
	oldConf := conf.Conf
	conf.Conf = conf.DefaultConfig("data")
	defer func() {
		conf.Conf = oldConf
	}()

	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")
	t.Setenv("https_proxy", "http://127.0.0.1:7890")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	client, err := NewOSSClient("https://oss-cn-hangzhou.aliyuncs.com", "test-access-key", "test-access-secret")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if client.HTTPClient == nil {
		t.Fatal("expected OSS client to use a custom HTTP client")
	}

	safe, ok := client.HTTPClient.Transport.(*safeTransport)
	if !ok {
		t.Fatalf("expected *safeTransport, got %T", client.HTTPClient.Transport)
	}
	transport, ok := safe.base.(*http.Transport)
	if !ok {
		t.Fatalf("expected safe transport to wrap *http.Transport, got %T", safe.base)
	}

	if transport.Proxy == nil {
		t.Fatal("expected proxy function to be configured")
	}

	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "oss-cn-hangzhou.aliyuncs.com"}}
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("expected no proxy lookup error, got %v", err)
	}
	if proxyURL == nil {
		t.Fatal("expected HTTPS proxy to be used")
	}
	if got, want := proxyURL.String(), "http://127.0.0.1:7890"; got != want {
		t.Fatalf("expected proxy %q, got %q", want, got)
	}
}
