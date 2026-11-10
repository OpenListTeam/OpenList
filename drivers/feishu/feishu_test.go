package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/adler32"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"golang.org/x/time/rate"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testSession(t *testing.T, handler http.HandlerFunc) *session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &session{cfg: Addition{AuthMode: "tenant", AppID: "app", AppSecret: "secret", DocumentFormat: "docx"}, ctx: ctx, cancel: cancel, generation: "generation", tenantToken: "tenant-token", tenantExpiry: time.Now().Add(time.Hour), limiter: rate.NewLimiter(rate.Inf, 1), exports: make(map[string]*exportEntry)}
	s.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "open.feishu.cn" {
			t.Errorf("credentials sent to unexpected host %s", req.URL.Host)
		}
		response := httptest.NewRecorder()
		handler(response, req)
		return response.Result(), nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	s.persist = func(Addition) error { return nil }
	return s
}

func success(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func TestTenantTokenConcurrentRefresh(t *testing.T) {
	var calls atomic.Int32
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/open-apis/auth/v3/tenant_access_token/internal" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected tenant token request")
		}
		io.WriteString(w, `{"code":0,"tenant_access_token":"new-tenant-token","expire":7200}`)
	})
	s.tenantExpiry = time.Time{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := s.accessToken(context.Background())
			if err != nil || token != "new-tenant-token" {
				t.Errorf("token=%q err=%v", token, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("got %d exchanges", calls.Load())
	}
}

func TestUserTokenRotationAndPersistenceRetry(t *testing.T) {
	var calls atomic.Int32
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "refresh_token" || body["refresh_token"] != "old-refresh" || body["client_id"] != "app" {
			t.Error("incorrect refresh request")
		}
		io.WriteString(w, `{"code":0,"access_token":"new-access","refresh_token":"new-refresh","expires_in":7200}`)
	})
	s.cfg.AuthMode = "user"
	s.cfg.RefreshToken = "old-refresh"
	var saves atomic.Int32
	s.persist = func(cfg Addition) error {
		if cfg.RefreshToken != "new-refresh" {
			t.Error("rotated refresh token not persisted")
		}
		if saves.Add(1) == 1 {
			return errors.New("database unavailable")
		}
		return nil
	}
	if _, err := s.accessToken(context.Background()); err == nil {
		t.Fatal("failed credential save was hidden")
	}
	token, err := s.accessToken(context.Background())
	if err != nil || token != "new-access" || calls.Load() != 1 || saves.Load() != 2 {
		t.Fatalf("token=%q err=%v calls=%d saves=%d", token, err, calls.Load(), saves.Load())
	}
}

func TestAuthorizationRequiresRenewableCredentials(t *testing.T) {
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"code":0,"access_token":"access","expires_in":7200}`)
	})
	s.cfg.AuthMode = "user"
	s.cfg.RefreshToken = "refresh"
	if _, err := s.accessToken(context.Background()); err == nil || !strings.Contains(err.Error(), "offline_access") {
		t.Fatalf("missing refresh token accepted: %v", err)
	}
	d := &Feishu{s: s}
	if err := d.CompleteAuthorization(context.Background(), "stale", "code"); err == nil {
		t.Fatal("stale authorization generation accepted")
	}
}

func TestAuthorizationCodeExchangeAndConcurrentUserRefresh(t *testing.T) {
	var calls atomic.Int32
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["client_secret"] != "secret" || body["client_id"] != "app" {
			t.Error("missing application authentication")
		}
		if body["grant_type"] == "authorization_code" && (body["code"] != "auth-code" || body["redirect_uri"] != "https://openlist.example/api/public/feishu/callback") {
			t.Error("invalid authorization code exchange")
		}
		io.WriteString(w, `{"code":0,"access_token":"user-access","refresh_token":"user-refresh","expires_in":7200}`)
	})
	s.cfg.AuthMode = "user"
	s.cfg.RedirectURI = "https://openlist.example/api/public/feishu/callback"
	d := &Feishu{s: s}
	if err := d.CompleteAuthorization(context.Background(), s.generation, "auth-code"); err != nil {
		t.Fatal(err)
	}
	s.cfg.TokenExpiresAt = 0
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := s.accessToken(context.Background())
			if err != nil || token != "user-access" {
				t.Errorf("user refresh: %s %v", token, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("expected code exchange and one refresh, got %d calls", calls.Load())
	}
}

func TestMutationsAreNotRetried(t *testing.T) {
	var calls atomic.Int32
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		io.WriteString(w, `{"code":1061001,"msg":"private-server-detail"}`)
	})
	err := s.json(context.Background(), "POST", "/drive/v1/files/create_folder", map[string]string{"name": "folder"}, nil)
	if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "private-server-detail") {
		t.Fatalf("unsafe mutation retry or error: calls=%d err=%v", calls.Load(), err)
	}
}

func TestListPaginationAndStableNames(t *testing.T) {
	var pages atomic.Int32
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		if r.Header.Get("Authorization") != "Bearer tenant-token" {
			t.Error("missing authorization")
		}
		if r.URL.Query().Get("page_token") == "" {
			success(w, map[string]any{"files": []fileInfo{{Token: "fileB", Name: "same.txt", Type: "file"}, {Token: "docA", Name: "note", Type: "docx"}}, "has_more": true, "next_page_token": "next"})
			return
		}
		success(w, map[string]any{"files": []fileInfo{{Token: "fileA", Name: "same.txt", Type: "file"}, {Token: "folderA", Name: "dir", Type: "folder"}, {Token: "badname", Name: "../escape", Type: "file"}}})
	})
	objects, err := s.objects(context.Background(), "root")
	if err != nil || len(objects) != 5 || pages.Load() != 2 {
		t.Fatalf("objects=%v err=%v", objects, err)
	}
	got := map[string]model.Obj{}
	for _, o := range objects {
		got[o.GetName()] = o
		if strings.Contains(o.GetName(), "/") {
			t.Fatal("unsafe path name exposed")
		}
	}
	if got["same [fileA].txt"] == nil || got["same [fileB].txt"] == nil || got["note.docx"] == nil || !got["dir"].IsDir() {
		t.Fatalf("unexpected names %v", got)
	}
	if got["note.docx"].GetSize() != -1 {
		t.Fatal("unknown size was fabricated")
	}
}

func TestListRejectsRepeatingCursor(t *testing.T) {
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		success(w, map[string]any{"files": []fileInfo{}, "has_more": true, "next_page_token": "same"})
	})
	if _, err := s.objects(context.Background(), "root"); err == nil {
		t.Fatal("pagination cycle accepted")
	}
}

func TestDownloadRangeAndSizeProbe(t *testing.T) {
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tenant-token" {
			t.Error("missing download authorization")
		}
		switch r.Header.Get("Range") {
		case "bytes=0-0":
			w.Header().Set("Content-Range", "bytes 0-0/6")
			w.WriteHeader(206)
			io.WriteString(w, "a")
		case "bytes=2-4":
			w.Header().Set("Content-Range", "bytes 2-4/6")
			w.WriteHeader(206)
			io.WriteString(w, "cde")
		default:
			t.Errorf("unexpected range %s", r.Header.Get("Range"))
		}
	})
	d := &Feishu{s: s}
	link, err := d.Link(context.Background(), &object{Object: model.Object{ID: "file", Size: -1}, Kind: "file"}, model.LinkArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if link.URL != "" || len(link.Header) != 0 || link.ContentLength != 6 {
		t.Fatalf("unsafe or incorrect link: %+v", link)
	}
	reader, err := link.RangeReader.RangeRead(context.Background(), http_range.Range{Start: 2, Length: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "cde" {
		t.Fatalf("body=%q err=%v", data, err)
	}
}

func TestDownloadIgnoredRangeAndMismatch(t *testing.T) {
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "6")
		io.WriteString(w, "abcdef")
	})
	r := &downloadReader{s: s, endpoint: "/drive/v1/files/file/download", size: 6}
	body, err := r.RangeRead(context.Background(), http_range.Range{Start: 2, Length: 2})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "cd" {
		t.Fatalf("ignored Range returned %q", data)
	}
	s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Range", "bytes 0-1/6")
		w.WriteHeader(206)
		io.WriteString(w, "ab")
		return w.Result(), nil
	})
	if _, err := r.RangeRead(context.Background(), http_range.Range{Start: 2, Length: 2}); err == nil {
		t.Fatal("mismatched range accepted")
	}
}

func TestDownloadRejectsRedirectAndAPIErrors(t *testing.T) {
	for _, status := range []int{302, 403, 200} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://other.example/file")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				io.WriteString(w, `{"code":1061004,"msg":"secret-private-value"}`)
			})
			_, err := s.probeSize(context.Background(), "/drive/v1/files/file/download")
			if err == nil || strings.Contains(err.Error(), "secret-private-value") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestExportConcurrentRequestsShareTask(t *testing.T) {
	var creates atomic.Int32
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			creates.Add(1)
			success(w, map[string]string{"ticket": "ticket"})
			return
		}
		if r.URL.Query().Get("token") != "doc" {
			t.Error("missing source document token")
		}
		success(w, map[string]any{"result": map[string]any{"job_status": 0, "file_token": "exported", "file_size": 123}})
	})
	o := &object{Object: model.Object{ID: "doc", Modified: time.Now()}, Kind: "docx"}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, size, err := s.export(context.Background(), o)
			if err != nil || token != "exported" || size != 123 {
				t.Errorf("export: %s %d %v", token, size, err)
			}
		}()
	}
	wg.Wait()
	if creates.Load() != 1 {
		t.Fatalf("created %d export jobs", creates.Load())
	}
}

func TestExportCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			success(w, map[string]string{"ticket": "ticket"})
			return
		}
		success(w, map[string]any{"result": map[string]any{"job_status": 2}})
		cancel()
	})
	_, _, err := s.runExport(ctx, &object{Object: model.Object{ID: "doc"}, Kind: "docx"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("export not canceled: %v", err)
	}
}

type testFile struct {
	model.FileStreamer
	data  *bytes.Reader
	name  string
	size  int64
	exist model.Obj
}

func (f *testFile) Read(p []byte) (int, error) { return f.data.Read(p) }
func (f *testFile) GetName() string            { return f.name }
func (f *testFile) GetSize() int64             { return f.size }
func (f *testFile) GetExist() model.Obj        { return f.exist }

func TestUploadSmallAndChunked(t *testing.T) {
	for _, size := range []int{17, (20 << 20) + 17} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			payload := bytes.Repeat([]byte("x"), size)
			var uploaded bytes.Buffer
			var parts int
			s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/open-apis/drive/v1/files":
					success(w, map[string]any{"files": []fileInfo{}})
				case "/open-apis/drive/v1/files/upload_prepare":
					success(w, map[string]any{"upload_id": "upload", "block_size": 4 << 20, "block_num": 1 + (size-1)/(4<<20)})
				case "/open-apis/drive/v1/files/upload_all", "/open-apis/drive/v1/files/upload_part":
					if r.ContentLength <= 0 {
						t.Error("missing multipart Content-Length")
					}
					if err := r.ParseMultipartForm(32 << 20); err != nil {
						t.Fatal(err)
					}
					defer r.MultipartForm.RemoveAll()
					file, _, err := r.FormFile("file")
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					data, err := io.ReadAll(file)
					if err != nil {
						t.Fatal(err)
					}
					if r.FormValue("size") != strconv.Itoa(len(data)) {
						t.Error("invalid part size")
					}
					if size > 20<<20 {
						if r.FormValue("seq") != strconv.Itoa(parts) {
							t.Error("invalid part sequence")
						}
						if r.FormValue("checksum") != strconv.FormatUint(uint64(adler32.Checksum(data)), 10) {
							t.Error("invalid checksum")
						}
						parts++
						success(w, map[string]any{})
					} else {
						success(w, map[string]string{"file_token": "uploaded"})
					}
					uploaded.Write(data)
				case "/open-apis/drive/v1/files/upload_finish":
					success(w, map[string]string{"file_token": "uploaded"})
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			})
			d := &Feishu{s: s}
			var progress []float64
			obj, err := d.Put(context.Background(), &object{Object: model.Object{ID: "folder", IsFolder: true}, Kind: "folder"}, &testFile{data: bytes.NewReader(payload), size: int64(size), name: "test.bin"}, func(p float64) { progress = append(progress, p) })
			if err != nil {
				t.Fatal(err)
			}
			if obj.GetID() != "uploaded" || !bytes.Equal(uploaded.Bytes(), payload) {
				t.Fatal("uploaded payload mismatch")
			}
			if len(progress) < 2 || progress[len(progress)-1] != 100 {
				t.Fatalf("missing upload progress: %v", progress)
			}
			for i := 1; i < len(progress); i++ {
				if progress[i] < progress[i-1] {
					t.Fatal("upload progress regressed")
				}
			}
		})
	}
}

func TestReadOnlyAndUploadConflict(t *testing.T) {
	var mutations atomic.Int32
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations.Add(1)
		}
		success(w, map[string]any{"files": []fileInfo{{Token: "existing", Name: "test.bin", Type: "file"}}})
	})
	d := &Feishu{s: s}
	folder := &object{Object: model.Object{ID: "folder", IsFolder: true}, Kind: "folder"}
	file := &testFile{data: bytes.NewReader([]byte("x")), size: 1, name: "test.bin"}
	if _, err := d.Put(context.Background(), folder, file, nil); err == nil {
		t.Fatal("conflicting upload accepted")
	}
	s.cfg.ReadOnly = true
	if _, err := d.Put(context.Background(), folder, file, nil); err == nil {
		t.Fatal("read-only upload accepted")
	}
	if _, err := d.MakeDir(context.Background(), folder, "dir"); err == nil {
		t.Fatal("read-only mkdir accepted")
	}
	if mutations.Load() != 0 {
		t.Fatal("mutation executed")
	}
}

func TestValidationAndDrop(t *testing.T) {
	for _, raw := range []string{"https://tenant.feishu.cn/drive/folder/folderToken?from=home", "folderToken"} {
		if token, err := normalizeRoot(raw); err != nil || token != "folderToken" {
			t.Errorf("root %q: %s %v", raw, token, err)
		}
	}
	for _, raw := range []string{"", "../escape", "https://feishu.cn.evil.test/drive/folder/token", "https://tenant.feishu.cn/wiki/token"} {
		if _, err := normalizeRoot(raw); err == nil {
			t.Errorf("unsafe root accepted: %s", raw)
		}
	}
	for _, raw := range []string{"http://public.example/callback", "https://user:secret@example.com/callback", "https://example.com/callback?x=1"} {
		if _, err := ValidateRedirectURI(raw); err == nil {
			t.Errorf("unsafe callback accepted: %s", raw)
		}
	}
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) { t.Error("request made after Drop") })
	d := &Feishu{s: s}
	if strings.Contains(fmt.Sprintf("%+v", d), "secret") {
		t.Fatal("driver logging exposes credentials")
	}
	if err := d.Drop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.request(context.Background(), "GET", "/drive/v1/files", nil, "", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("old session not canceled: %v", err)
	}
}

func TestUserDefaultRootAndModeValidation(t *testing.T) {
	for _, input := range []string{"", "  ", "https://tenant.feishu.cn/drive/me/"} {
		root, err := normalizeRootForMode(input, "user")
		if err != nil || root != "" {
			t.Fatalf("user root %q: %q %v", input, root, err)
		}
		if _, err := normalizeRootForMode(input, "tenant"); err == nil {
			t.Fatalf("application root %q accepted", input)
		}
	}
	var calls atomic.Int32
	s := testSession(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/open-apis/drive/explorer/v2/root_folder/meta" {
			t.Errorf("unexpected root request: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer user-access" {
			t.Error("wrong root identity")
		}
		success(w, map[string]string{"token": "userRoot"})
	})
	s.cfg.AuthMode = "user"
	s.cfg.AccessToken = "user-access"
	s.cfg.TokenExpiresAt = time.Now().Add(time.Hour).Unix()
	d := &Feishu{s: s}
	for range 2 {
		root, err := d.GetRoot(context.Background())
		if err != nil || root.GetID() != "userRoot" {
			t.Fatalf("root=%v err=%v", root, err)
		}
	}
	if calls.Load() != 1 || s.cfg.RootFolderID != "" {
		t.Fatal("user root resolution was not cached separately from settings")
	}
}

func TestNewUserStorageCanBeSavedBeforeAuthorization(t *testing.T) {
	d := &Feishu{Addition: Addition{AuthMode: "user", AppID: "app", AppSecret: "secret", RedirectURI: "https://example.com/api/public/feishu/callback"}}
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("could not save pending OAuth configuration: %v", err)
	}
	t.Cleanup(func() { d.Drop(context.Background()) })
	if cfg, _, err := d.OAuthConfig(); err != nil || cfg.RootFolderID != "" {
		t.Fatalf("pending authorization configuration: %v %v", cfg.RootFolderID, err)
	}
}
