package foxel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
)

func writeData(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok", "data": data})
}

func newTestDriver(t *testing.T, handler http.HandlerFunc) *Foxel {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/me" {
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("unexpected authorization: %q", r.Header.Get("Authorization"))
			}
			writeData(w, map[string]string{"username": "test"})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	d := &Foxel{Addition: Addition{Address: srv.URL, Token: "Bearer test-token"}, client: srv.Client()}
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return d
}

func TestInitRejectsInvalidConfiguration(t *testing.T) {
	cases := []Addition{
		{Address: "ftp://example.com", Token: "token"},
		{Address: "https://user:pass@example.com", Token: "token"},
		{Address: "https://example.com?api=1", Token: "token"},
		{Address: "https://example.com#fragment", Token: "token"},
		{Address: "https://example.com"},
		{Address: "https://example.com", Username: "test"},
		{Address: "https://example.com", Password: "test", Token: "token"},
		{Address: "https://example.com", Token: "token", PageSize: 501},
		{Address: "https://example.com", Token: "token", PageSize: -1},
		{Address: "https://example.com", Token: "token", LinkExpire: -1},
	}
	for i, addition := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			d := &Foxel{Addition: addition, client: &http.Client{}}
			if err := d.Init(context.Background()); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

func TestAuthenticationRefreshIsConcurrentAndDoesNotRetryForbidden(t *testing.T) {
	var logins atomic.Int32
	var expired, forbidden atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Error("login must use an OAuth2 form POST")
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("username") != "用户+test" || r.Form.Get("password") != "p&+ word" {
				t.Error("login credentials were not form encoded")
			}
			n := logins.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": fmt.Sprintf("token-%d", n), "token_type": "bearer"})
			return
		}
		if expired.Load() && r.Header.Get("Authorization") == "Bearer token-1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"detail":"Expired token"}`)
			return
		}
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer token-%d", logins.Load()) {
			t.Errorf("unexpected access token: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/api/auth/me" {
			writeData(w, map[string]string{"username": "用户+test"})
			return
		}
		if forbidden.Load() {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"detail":"Read permission denied"}`)
			return
		}
		writeData(w, map[string]any{"entries": []entry{}, "pagination": map[string]any{"mode": "paged", "pages": 0}})
	}))
	t.Cleanup(srv.Close)
	d := &Foxel{Addition: Addition{
		Address: srv.URL + "/", Username: "用户+test", Password: "p&+ word", Token: "ignored",
	}, client: srv.Client()}
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.RootFolderPath != "/" || d.PageSize != 200 || d.LinkExpire != 3600 || logins.Load() != 1 {
		t.Fatal("initialization did not apply defaults and login")
	}
	expired.Store(true)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.List(context.Background(), &model.Object{Path: "/"}, model.ListArgs{}); err != nil {
				t.Errorf("List after expiry: %v", err)
			}
		}()
	}
	wg.Wait()
	if logins.Load() != 2 {
		t.Fatalf("concurrent requests caused %d logins, want 2", logins.Load())
	}
	forbidden.Store(true)
	_, err := d.List(context.Background(), &model.Object{Path: "/"}, model.ListArgs{})
	if err == nil || !strings.Contains(err.Error(), "Read permission denied") || logins.Load() != 2 {
		t.Fatalf("403 must be returned without reauthentication, got %v", err)
	}
}

func TestListPaginationPreservesPathsAndSkipsPermissionFilteredPages(t *testing.T) {
	var pages []string
	var pagesMu sync.Mutex
	d := newTestDriver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing Bearer token")
		}
		if r.URL.Path == "/api/fs/drive" {
			pageNum := r.URL.Query().Get("page")
			pagesMu.Lock()
			pages = append(pages, pageNum)
			pagesMu.Unlock()
			var entries []entry
			if pageNum == "2" {
				entries = []entry{{Name: "子目录 #?%", IsDir: true, Mtime: 1700000000}}
			}
			writeData(w, map[string]any{
				"entries":    entries,
				"pagination": map[string]any{"mode": "paged", "pages": 3, "page_size": 2},
			})
			return
		}
		if r.URL.Path == "/api/fs/drive/子目录 #?%" {
			if r.URL.RawQuery != "page=1&page_size=2&sort_by=name&sort_order=asc" {
				t.Errorf("filename changed the query string: %s", r.URL.RawQuery)
			}
			writeData(w, map[string]any{
				"entries":    []entry{{Name: "file+.txt", Size: 7, Mtime: 1700000001}},
				"pagination": map[string]any{"mode": "paged", "pages": 1},
			})
			return
		}
		t.Errorf("unexpected listing path: %q", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	d.PageSize = 2
	root := &model.Object{Path: "/drive", IsFolder: true}
	children, err := d.List(context.Background(), root, model.ListArgs{})
	if err != nil || len(children) != 1 {
		t.Fatalf("List: %v, children=%v", err, children)
	}
	pagesMu.Lock()
	gotPages := append([]string(nil), pages...)
	pagesMu.Unlock()
	if !reflect.DeepEqual(gotPages, []string{"1", "2", "3"}) {
		t.Fatalf("skipped a permission-filtered page: %v", gotPages)
	}
	child := children[0]
	if child.GetPath() != "/drive/子目录 #?%" || child.GetID() != child.GetPath() || !child.IsDir() || child.ModTime().Unix() != 1700000000 {
		t.Fatalf("incorrect directory object: %+v", child)
	}
	files, err := d.List(context.Background(), child, model.ListArgs{})
	if err != nil || len(files) != 1 || files[0].GetPath() != "/drive/子目录 #?%/file+.txt" || files[0].GetSize() != 7 {
		t.Fatalf("nested listing: %v, files=%v", err, files)
	}
}

func TestCursorPagination(t *testing.T) {
	var cursors []string
	var cursorsMu sync.Mutex
	d := newTestDriver(t, func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursorsMu.Lock()
		cursors = append(cursors, cursor)
		cursorsMu.Unlock()
		entries := []entry{}
		next := ""
		switch cursor {
		case "":
			entries = []entry{{Name: "first.txt"}}
			next = "next/+?=#"
		case "next/+?=#":
			next = "last"
		case "last":
			entries = []entry{{Name: "last.txt"}}
		default:
			t.Errorf("unexpected cursor: %q", cursor)
		}
		writeData(w, map[string]any{
			"entries":    entries,
			"pagination": map[string]any{"mode": "cursor", "has_next": next != "", "next_cursor": next},
		})
	})
	files, err := d.List(context.Background(), &model.Object{Path: "/"}, model.ListArgs{})
	cursorsMu.Lock()
	defer cursorsMu.Unlock()
	if err != nil || len(files) != 2 || !reflect.DeepEqual(cursors, []string{"", "next/+?=#", "last"}) {
		t.Fatalf("cursor listing: files=%v, cursors=%v, error=%v", files, cursors, err)
	}
}

func TestListingRejectsRepeatedCursor(t *testing.T) {
	var calls atomic.Int32
	d := newTestDriver(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeData(w, map[string]any{"entries": []entry{}, "pagination": map[string]any{
			"mode": "cursor", "has_next": true, "next_cursor": "repeated",
		}})
	})
	_, err := d.List(context.Background(), &model.Object{Path: "/"}, model.ListArgs{})
	if err == nil || !strings.Contains(err.Error(), "repeated cursor") || calls.Load() != 2 {
		t.Fatalf("repeated cursor should fail after two requests: %v (%d calls)", err, calls.Load())
	}
}

func TestLinkDownloadsOriginalBytesWithRange(t *testing.T) {
	name := "照片 #?%+.CR3"
	original := []byte{0, 1, 2, 3, 4, 5, 6, 255}
	d := newTestDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/temp-link/drive/" + name:
			if r.URL.Query().Get("expires_in") != "3600" {
				t.Error("temporary link lifetime was not sent")
			}
			writeData(w, tempLink{Token: "signed", URL: "/api/fs/public/signed/" + url.PathEscape(name)})
		case "/api/fs/download-public/signed/" + name:
			if r.Header.Get("Authorization") != "" {
				t.Error("public downloads must not expose the access token")
			}
			if r.Header.Get("Range") != "bytes=2-6" {
				t.Error("Range header was not preserved")
			}
			w.Header().Set("Content-Range", "bytes 2-6/8")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(original[2:7])
		default:
			t.Errorf("download used an unexpected endpoint: %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	link, err := d.Link(context.Background(), &model.Object{Path: "/drive/" + name, Name: name}, model.LinkArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if link.Expiration == nil || *link.Expiration != 3570*time.Second {
		t.Fatalf("incorrect cache expiry: %v", link.Expiration)
	}
	req, _ := http.NewRequest(http.MethodGet, link.URL, nil)
	req.Header.Set("Range", "bytes=2-6")
	res, err := d.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusPartialContent || !bytes.Equal(data, original[2:7]) {
		t.Fatalf("original range bytes changed: status=%d data=%v", res.StatusCode, data)
	}
}

func TestDownloadURLHonorsFileDomainAndProxyPrefix(t *testing.T) {
	d := &Foxel{Addition: Addition{Address: "https://foxel.example/base"}}
	cases := []struct{ input, want string }{
		{"/api/fs/public/token/a%23b.txt", "https://foxel.example/base/api/fs/download-public/token/a%23b.txt"},
		{"https://files.example/prefix/api/fs/public/token/a%23b.txt?signature=abc", "https://files.example/prefix/api/fs/download-public/token/a%23b.txt?signature=abc"},
	}
	for _, tc := range cases {
		got, err := d.downloadURL(tempLink{URL: tc.input}, "a#b.txt")
		if err != nil || got != tc.want {
			t.Fatalf("downloadURL(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
}

func TestFileOperationsUseFullPaths(t *testing.T) {
	src := &model.Object{Path: "/drive/a#?%.txt", Name: "a#?%.txt"}
	dst := &model.Object{Path: "/other/dest", IsFolder: true}
	var calls atomic.Int32
	d := newTestDriver(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method == http.MethodDelete {
			if r.URL.Path != "/api/fs"+src.Path {
				t.Errorf("wrong delete path: %s", r.URL.Path)
			}
			writeData(w, map[string]bool{"deleted": true})
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		operation := path.Base(r.URL.Path)
		if operation == "mkdir" {
			if body["path"] != "/other/dest/new #?%" {
				t.Errorf("wrong mkdir path: %v", body)
			}
			writeData(w, map[string]bool{"created": true})
			return
		}
		wantDst := "/other/dest/a#?%.txt"
		if operation == "rename" {
			wantDst = "/drive/new #?%.txt"
		}
		if body["src"] != src.Path || body["dst"] != wantDst || r.URL.Query().Get("overwrite") != "false" {
			t.Errorf("wrong %s request: %v, query=%v", operation, body, r.URL.Query())
		}
		writeData(w, map[string]bool{"moved": true, "copied": true, "renamed": true})
	})
	ctx := context.Background()
	for _, err := range []error{
		d.MakeDir(ctx, dst, "new #?%"), d.Move(ctx, src, dst),
		d.Copy(ctx, src, dst), d.Rename(ctx, src, "new #?%.txt"), d.Remove(ctx, src),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 5 {
		t.Fatalf("expected five file operations, got %d", calls.Load())
	}
	if err := d.Rename(ctx, src, "../escape"); err == nil || calls.Load() != 5 {
		t.Fatal("invalid leaf name must not send a request")
	}
}

func TestQueuedTransfersWaitAndPropagateFailures(t *testing.T) {
	for _, status := range []string{"success", "failed", "running", "unexpected"} {
		t.Run(status, func(t *testing.T) {
			var polls atomic.Int32
			d := newTestDriver(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/fs/copy" {
					writeData(w, transferResult{Queued: true, TaskID: "job"})
					return
				}
				if r.URL.Path != "/api/tasks/queue/job" {
					t.Errorf("unexpected task endpoint: %s", r.URL.Path)
				}
				n := polls.Add(1)
				current := status
				if status == "success" && n == 1 {
					current = "pending"
				}
				writeData(w, taskStatus{Status: current, Error: "quota exceeded"})
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if status == "running" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			}
			defer cancel()
			err := d.Copy(ctx, &model.Object{Path: "/src/a", Name: "a"}, &model.Object{Path: "/dst"})
			switch status {
			case "success":
				if err != nil || polls.Load() != 2 {
					t.Fatalf("queued copy finished too early: %v, polls=%d", err, polls.Load())
				}
			case "failed":
				if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
					t.Fatalf("task error was lost: %v", err)
				}
			case "running":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("task polling ignored cancellation: %v", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), "unexpected status") {
					t.Fatalf("unknown status was accepted: %v", err)
				}
			}
		})
	}
}

func TestUploadStreamsBytesAndReportsProgress(t *testing.T) {
	for _, content := range []string{"original file bytes", ""} {
		t.Run(fmt.Sprintf("size-%d", len(content)), func(t *testing.T) {
			name := "上传 #?%.bin"
			d := newTestDriver(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.Path != "/api/fs/upload-raw/disk/"+name ||
					r.URL.Query().Get("overwrite") != "true" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("incorrect raw upload: %s %s", r.Method, r.URL)
				}
				data, err := io.ReadAll(r.Body)
				if err != nil || string(data) != content {
					t.Errorf("uploaded bytes changed: %q (%v)", data, err)
				}
				// Providers may return an adjusted filename.
				writeData(w, uploadResult{Uploaded: true, Path: "/disk/actual.bin", Size: int64(len(data))})
			})
			var progress []float64
			var progressMu sync.Mutex
			file := &stream.FileStream{
				Ctx:    context.Background(),
				Obj:    &model.Object{Name: name, Size: int64(len(content)), Modified: time.Unix(1700000000, 0)},
				Reader: strings.NewReader(content),
			}
			obj, err := d.Put(context.Background(), &model.Object{Path: "/disk"}, file, func(p float64) {
				progressMu.Lock()
				defer progressMu.Unlock()
				progress = append(progress, p)
			})
			if err != nil {
				t.Fatal(err)
			}
			if obj.GetPath() != "/disk/actual.bin" || obj.GetName() != "actual.bin" || obj.GetSize() != int64(len(content)) {
				t.Fatalf("upload result was lost: %+v", obj)
			}
			progressMu.Lock()
			defer progressMu.Unlock()
			if len(progress) == 0 || progress[len(progress)-1] != 100 {
				t.Fatalf("upload did not finish progress: %v", progress)
			}
			for _, p := range progress {
				if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 100 {
					t.Fatalf("invalid upload progress: %v", progress)
				}
			}
		})
	}
}

func TestErrorsAndTokenOnlyExpiry(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, want string
	}{
		{401, `{"detail":"Token expired"}`, "Token expired"},
		{422, `{"detail":[{"msg":"Invalid path"}]}`, "Invalid path"},
		{200, `{"code":1,"msg":"Provider unavailable","data":null}`, "Provider unavailable"},
		{200, `{"msg":"missing code"}`, "missing its status code"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			var calls atomic.Int32
			d := newTestDriver(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := d.List(context.Background(), &model.Object{Path: "/"}, model.ListArgs{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls.Load() != 1 {
				t.Fatalf("incorrect error or retry: %v, calls=%d", err, calls.Load())
			}
		})
	}
}

func TestUploadRefreshesBeforeReadingNonSeekableStream(t *testing.T) {
	var logins, reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			if reads.Load() != 0 {
				t.Error("upload bytes were consumed before login")
			}
			logins.Add(1)
			_, _ = io.WriteString(w, `{"access_token":"fresh","token_type":"bearer"}`)
		case "/api/auth/me":
			if r.Header.Get("Authorization") != "Bearer fresh" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"detail":"Expired token"}`)
				return
			}
			writeData(w, nil)
		case "/api/fs/upload-raw/disk/file.bin":
			data, _ := io.ReadAll(r.Body)
			if r.Header.Get("Authorization") != "Bearer fresh" || string(data) != "original" {
				t.Error("authenticated upload lost its original bytes")
			}
			writeData(w, uploadResult{Uploaded: true, Path: "/disk/file.bin", Size: int64(len(data))})
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	d := &Foxel{Addition: Addition{Address: srv.URL, Username: "test", Password: "password"}, client: srv.Client()}
	// A running driver may hold an expired token immediately before a raw upload.
	d.setToken("expired")
	reader := &countingReader{Reader: strings.NewReader("original"), reads: &reads}
	file := &stream.FileStream{Obj: &model.Object{Name: "file.bin", Size: 8}, Reader: reader}
	_, err := d.Put(context.Background(), &model.Object{Path: "/disk"}, file, nil)
	if err != nil || logins.Load() != 1 || reads.Load() == 0 {
		t.Fatalf("preflight refresh: error=%v logins=%d reads=%d", err, logins.Load(), reads.Load())
	}
}

type countingReader struct {
	io.Reader
	reads *atomic.Int32
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads.Add(1)
	return r.Reader.Read(p)
}

func TestCanceledUploadDoesNotConsumeInput(t *testing.T) {
	d := newTestDriver(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("canceled upload reached the file endpoint")
	})
	var reads atomic.Int32
	file := &stream.FileStream{
		Obj:    &model.Object{Name: "file.bin", Size: 8},
		Reader: &countingReader{Reader: strings.NewReader("original"), reads: &reads},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Put(ctx, &model.Object{Path: "/disk"}, file, nil)
	if !errors.Is(err, context.Canceled) || reads.Load() != 0 {
		t.Fatalf("canceled upload consumed input: %v, reads=%d", err, reads.Load())
	}
}
