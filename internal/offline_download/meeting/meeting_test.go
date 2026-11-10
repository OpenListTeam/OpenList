package meeting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/google/uuid"
)

// 空 Cookie 必须允许提交匿名任务；有凭据的混合平台请求在入队前拒绝。
func TestOptionalCookieAndQueueEncryption(t *testing.T) {
	old := conf.DataDir
	conf.DataDir = t.TempDir()
	t.Cleanup(func() { conf.DataDir = old })
	raw := "https://meeting.tencent.com/cw/test"
	for _, cookie := range []string{"", "session=private-only"} {
		task := &tool.DownloadTask{Url: raw, SourceCookie: cookie, DeletePolicy: tool.DeleteOnUploadSucceed}
		task.ID = uuid.NewString()
		if err := Default.PrepareTask(task); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		if task.SourceCookie != "" || strings.Contains(string(data), "private-only") {
			t.Fatal("plaintext persisted")
		}
		var restored tool.DownloadTask
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		got, err := unsealCookie(task.ID, restored.MeetingCookie)
		if err != nil || got != cookie {
			t.Fatal("cookie did not survive queue serialization")
		}
		if cookie != "" {
			if _, err := unsealCookie(uuid.NewString(), task.MeetingCookie); err == nil {
				t.Fatal("accepted ciphertext for another task")
			}
		}
	}
	if ValidateBatch([]string{raw, "https://tenant.feishu.cn/minutes/test"}, "session=x") == nil {
		t.Fatal("accepted mixed cookie scope")
	}
	if ValidateBatch([]string{raw}, "session=x\r\nX-Injection:y") == nil {
		t.Fatal("accepted newline")
	}
	if ValidateBatch([]string{"https://meeting.tencent.com.evil.example/cw/test"}, "") == nil {
		t.Fatal("accepted lookalike site")
	}
}

// 用真正 HTTP 请求验证匿名、Cookie、Range、跨源凭据和随机资源地址。
func TestMediaRelayAuthentication(t *testing.T) {
	var source *httptest.Server
	source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/private.mp4" && r.Header.Get("Cookie") != "session=fixture" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/public.mp4" && r.Header.Get("Cookie") != "" {
			t.Error("unexpected cookie")
		}
		if r.Header.Get("Range") == "bytes=1-3" {
			w.Header().Set("Content-Range", "bytes 1-3/6")
			w.WriteHeader(206)
			io.WriteString(w, "bcd")
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		io.WriteString(w, "abcdef")
	}))
	defer source.Close()
	for _, tc := range []struct {
		file, cookie string
		want         int
	}{{"/public.mp4", "", 206}, {"/private.mp4", "session=fixture", 206}, {"/private.mp4", "", 401}} {
		r, err := newRelay(context.Background(), mediaSource{URL: source.URL + tc.file, Kind: "mp4", Headers: map[string]string{"Cookie": tc.cookie}}, http.DefaultTransport)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest("GET", r.URL, nil)
		req.Header.Set("Range", "bytes=1-3")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("status %d want %d", resp.StatusCode, tc.want)
		}
		if tc.want == 206 && string(body) != "bcd" {
			t.Fatal("wrong range data")
		}
		forged, _ := url.Parse(r.URL)
		forged.Path = "/unknown/media.mp4"
		resp, _ = http.Get(forged.String())
		if resp.StatusCode != 403 {
			t.Fatal("accepted unknown resource")
		}
		resp.Body.Close()
		r.Close()
	}
}

func TestHLSManifestAndCredentialBoundary(t *testing.T) {
	r, err := newRelay(context.Background(), mediaSource{URL: "https://media.example/list.m3u8", Kind: "hls", Headers: map[string]string{"Cookie": "session=secret", "Authorization": "Bearer secret", "Referer": "https://meeting.tencent.com/"}}, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	base, _ := url.Parse("https://media.example/path/list.m3u8")
	raw := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6,\nsegment.ts\n#EXT-X-ENDLIST\n"
	output, err := r.manifest(raw, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, r.prefix) || strings.Contains(output, "https://media.example") {
		t.Fatal("manifest resources bypassed relay")
	}
	if _, err := r.manifest(strings.ReplaceAll(raw, "#EXT-X-ENDLIST", ""), base); err == nil {
		t.Fatal("accepted live playlist")
	}
	req, _ := http.NewRequest("GET", "https://other.example/media.ts", nil)
	r.sourceHeaders(req)
	if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" || req.Header.Get("Referer") == "" {
		t.Fatal("cross-origin header boundary failed")
	}
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "::1", "fc00::1"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Fatal("accepted private media IP", ip)
		}
	}
}

func TestCacheIntegrity(t *testing.T) {
	work := t.TempDir()
	final := filepath.Join(t.TempDir(), "meeting.mp4")
	ctx := context.Background()
	if err := os.WriteFile(final, []byte("verified media"), 0600); err != nil {
		t.Fatal(err)
	}
	sum, size, err := digest(ctx, final)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cacheRecord{SHA256: sum, Size: size})
	if err := atomicFile(filepath.Join(work, "complete.json"), raw); err != nil {
		t.Fatal(err)
	}
	if !validCache(ctx, work, final) {
		t.Fatal("did not reuse verified media")
	}
	os.WriteFile(final, []byte("damaged media!"), 0600)
	if validCache(ctx, work, final) {
		t.Fatal("accepted damaged cache")
	}
}
