package meeting

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// relay 只监听当前进程所在容器的回环接口。下载器看到的是短期随机地址，
// 不会从命令行或下载日志暴露平台 Cookie；这里不对外注册 OpenList 路由。
type relay struct {
	URL       string
	server    *http.Server
	listener  net.Listener
	client    *http.Client
	source    mediaSource
	ctx       context.Context
	cancel    context.CancelFunc
	prefix    string
	mu        sync.Mutex
	resources map[string]string
}

func mediaURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.User != nil || u.Hostname() == "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || len(raw) > 16384 {
		return nil, fmt.Errorf("视频地址无效，仅支持 HTTP(S) 媒体")
	}
	return u, nil
}

// 公网媒体下载使用检查后的同一组 IP 拨号，不读取系统代理，也不允许
// 重定向到内网、回环或本地文件。测试夹具可显式传入独立 Transport。
func publicTransport() *http.Transport {
	t := &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 8, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 30 * time.Second}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("媒体域名解析失败")
		}
		for _, ip := range ips {
			ip = ip.Unmap()
			if !publicIP(ip) {
				return nil, fmt.Errorf("不允许访问内网媒体地址")
			}
		}
		dialer := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		for _, ip := range ips {
			if c, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port)); e == nil {
				return c, nil
			}
		}
		return nil, fmt.Errorf("媒体源连接失败")
	}
	return t
}
func publicIP(ip netip.Addr) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !netip.MustParsePrefix("198.18.0.0/15").Contains(ip)
}

func newRelay(ctx context.Context, source mediaSource, transport http.RoundTripper) (*relay, error) {
	if _, err := mediaURL(source.URL); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		l.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &relay{listener: l, source: source, ctx: ctx, cancel: cancel, prefix: "/" + hex.EncodeToString(token) + "/", resources: map[string]string{}}
	if transport == nil {
		transport = publicTransport()
	}
	r.client = &http.Client{Transport: transport, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) > 8 {
			return fmt.Errorf("too many media redirects")
		}
		if _, e := mediaURL(next.URL.String()); e != nil {
			return e
		}
		next.Header = http.Header{}
		r.sourceHeaders(next)
		if v := via[0].Header.Get("Range"); v != "" {
			next.Header.Set("Range", v)
		}
		return nil
	}}
	r.URL = "http://" + l.Addr().String() + r.resource(source.URL)
	r.server = &http.Server{Handler: http.HandlerFunc(r.serve), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() { _ = r.server.Serve(l) }()
	return r, nil
}
func (r *relay) Close() {
	r.cancel()
	_ = r.server.Close()
	if t, ok := r.client.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}

// 映射中只登记源地址和已解析清单中的资源，不接受调用者传入目标 URL。
func (r *relay) resource(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	u, _ := url.Parse(raw)
	suffix := strings.ToLower(path.Ext(u.Path))
	if suffix != ".m3u8" && suffix != ".mp4" && suffix != ".m4s" && suffix != ".key" && suffix != ".aac" {
		suffix = ".ts"
	}
	if raw == r.source.URL {
		if r.source.Kind == "hls" {
			suffix = ".m3u8"
		} else {
			suffix = ".mp4"
		}
	}
	key := r.prefix + hex.EncodeToString(sum[:]) + suffix
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resources[key] = raw
	return key
}
func (r *relay) sourceHeaders(req *http.Request) {
	root, _ := url.Parse(r.source.URL)
	same := root.Scheme == req.URL.Scheme && strings.EqualFold(root.Host, req.URL.Host)
	for name, value := range r.source.Headers {
		key := strings.ToLower(name)
		if key == "user-agent" || key == "referer" || key == "origin" || (same && (key == "cookie" || key == "authorization" || (strings.HasPrefix(key, "x-") && !strings.HasPrefix(key, "x-forwarded-")))) {
			req.Header.Set(name, value)
		}
	}
	req.Header.Set("Accept-Encoding", "identity")
}

// 每次请求均沿用任务取消上下文。HLS 资源先重写再交给下载器，因此分片、
// AES-128 密钥和独立音轨也通过相同的凭据边界及网络校验。
func (r *relay) serve(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" && req.Method != "HEAD" {
		http.Error(w, "method unavailable", 405)
		return
	}
	r.mu.Lock()
	raw, ok := r.resources[req.URL.Path]
	r.mu.Unlock()
	if !ok || req.URL.RawQuery != "" {
		http.Error(w, "resource unavailable", 403)
		return
	}
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	up, err := http.NewRequestWithContext(ctx, req.Method, raw, nil)
	if err != nil {
		http.Error(w, "media unavailable", 502)
		return
	}
	r.sourceHeaders(up)
	manifestPath := strings.HasSuffix(req.URL.Path, ".m3u8")
	if v := req.Header.Get("Range"); v != "" && !manifestPath {
		up.Header.Set("Range", v)
	}
	resp, err := r.client.Do(up)
	if err != nil {
		http.Error(w, "media unavailable", 502)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		http.Error(w, "media unavailable", resp.StatusCode)
		return
	}
	if resp.ContentLength > 100<<30 {
		http.Error(w, "media exceeds size limit", 413)
		return
	}
	if e := resp.Header.Get("Content-Encoding"); e != "" && e != "identity" {
		http.Error(w, "unexpected media encoding", 502)
		return
	}
	manifest := manifestPath || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	if manifest && req.Method == "GET" {
		content, e := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
		if e != nil || len(content) > 2<<20 {
			http.Error(w, "manifest unavailable", 502)
			return
		}
		body, e := r.manifest(string(content), resp.Request.URL)
		if e != nil {
			http.Error(w, "not a supported completed video", 422)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.WriteString(w, body)
		return
	}
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(key); v != "" {
			w.Header().Set(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if req.Method == "GET" {
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 100<<30))
	}
}

var uriAttribute = regexp.MustCompile(`URI="([^"]+)"`)

func (r *relay) manifest(raw string, base *url.URL) (string, error) {
	if !strings.HasPrefix(strings.TrimSpace(raw), "#EXTM3U") {
		return "", fmt.Errorf("invalid manifest")
	}
	if strings.Contains(raw, "#EXTINF:") && !strings.Contains(raw, "#EXT-X-ENDLIST") {
		return "", fmt.Errorf("video has not ended")
	}
	if strings.Contains(raw, "METHOD=SAMPLE-AES") || strings.Contains(raw, "KEYFORMAT=") || strings.Contains(raw, "{$") {
		return "", fmt.Errorf("unsupported protected or variable manifest")
	}
	link := func(value string) (string, error) {
		ref, e := url.Parse(value)
		if e != nil {
			return "", e
		}
		target := base.ResolveReference(ref)
		if _, e = mediaURL(target.String()); e != nil {
			return "", e
		}
		r.mu.Lock()
		n := len(r.resources)
		r.mu.Unlock()
		if n >= 100000 {
			return "", fmt.Errorf("too many media resources")
		}
		return "http://" + r.listener.Addr().String() + r.resource(target.String()), nil
	}
	lines := strings.Split(raw, "\n")
	total := 0
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if !strings.HasPrefix(trim, "#") {
			v, e := link(trim)
			if e != nil {
				return "", e
			}
			lines[i] = v
		} else {
			var linkErr error
			lines[i] = uriAttribute.ReplaceAllStringFunc(line, func(attr string) string {
				v, e := link(uriAttribute.FindStringSubmatch(attr)[1])
				if e != nil {
					linkErr = e
					return ""
				}
				return `URI="` + v + `"`
			})
			if linkErr != nil {
				return "", linkErr
			}
		}
		total += len(lines[i])
		if total > 16<<20 {
			return "", fmt.Errorf("manifest too large")
		}
	}
	return strings.Join(lines, "\n"), nil
}
