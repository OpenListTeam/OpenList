package ctfile

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers / mock
// ---------------------------------------------------------------------------

// buildServer creates a test HTTP server that returns pre-scripted responses in
// sequence. Each response is a JSON string (already complete). The variadic
// responses are consumed one at a time; the last one is reused for all
// subsequent requests.
func buildServer(t *testing.T, responses ...string) (*httptest.Server, *[]string) {
	t.Helper()
	calls := new([]string)
	idx := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, string(body))

		resp := responses[idx]
		if idx < len(responses)-1 {
			idx++
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(resp))
	}))
	return srv, calls
}

// driverWithBase returns a minimal CtFile driver pointing at the test server.
func driverWithBase(baseURL string) *CtFile {
	d := &CtFile{}
	d.Session = "test-session"
	d.BaseURL = baseURL
	d.CloudType = "public"
	d.OrderBy = "name"
	d.OrderDirection = "asc"
	return d
}

// ---------------------------------------------------------------------------
// StripIDPrefix
// ---------------------------------------------------------------------------

func TestStripIDPrefix(t *testing.T) {
	cases := []struct {
		in  string
		out string
	}{
		{"d123", "123"},
		{"f456", "456"},
		{"abc", "abc"},
		{"d", "d"},    // no digit follows → keep as-is
		{"f", "f"},    // no digit follows → keep as-is
		{"d0", "0"},
		{"", ""},
		{"123", "123"},
	}
	for _, tc := range cases {
		got := stripIDPrefix(tc.in)
		if got != tc.out {
			t.Errorf("stripIDPrefix(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}

// ---------------------------------------------------------------------------
// callWithId dual-try: 400 → retry with alternate id form
// ---------------------------------------------------------------------------

func TestCallWithId_DualTry(t *testing.T) {
	// First call returns code 400; second returns code 200 with a name field.
	srv, calls := buildServer(t,
		`{"code":400,"message":"not found"}`,
		`{"code":200,"name":"Photos"}`,
	)
	defer srv.Close()

	d := driverWithBase(srv.URL)
	ctx := context.Background()

	// prefer = "raw" → first attempt with "d123", fallback with "123"
	result, err := d.callWithId(ctx, "/public/folder/meta", "folder_id", "d123", nil, "raw")
	if err != nil {
		t.Fatalf("callWithId returned error: %v", err)
	}
	if toString(result["name"]) != "Photos" {
		t.Errorf("expected name=Photos, got %v", result["name"])
	}
	if len(*calls) != 2 {
		t.Fatalf("expected 2 HTTP calls, got %d", len(*calls))
	}
	if !strings.Contains((*calls)[0], `"d123"`) {
		t.Errorf("first call should use raw id d123, got: %s", (*calls)[0])
	}
	if !strings.Contains((*calls)[1], `"123"`) {
		t.Errorf("second call should use stripped id 123, got: %s", (*calls)[1])
	}
}

// ---------------------------------------------------------------------------
// callWithId: prefer stripped → file/fetch_url pattern
// ---------------------------------------------------------------------------

func TestCallWithId_PreferStripped(t *testing.T) {
	// First call (stripped "456") returns 400; second (raw "f456") returns 200.
	srv, calls := buildServer(t,
		`{"code":400,"message":"param error"}`,
		`{"code":200,"download_url":"https://cdn.example.com/file"}`,
	)
	defer srv.Close()

	d := driverWithBase(srv.URL)
	ctx := context.Background()

	result, err := d.callWithId(ctx, "/public/file/fetch_url", "file_id", "f456", nil, "stripped")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if toString(result["download_url"]) != "https://cdn.example.com/file" {
		t.Errorf("unexpected download_url: %v", result["download_url"])
	}
	if len(*calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(*calls))
	}
	if !strings.Contains((*calls)[0], `"456"`) {
		t.Errorf("first call should use stripped 456: %s", (*calls)[0])
	}
	if !strings.Contains((*calls)[1], `"f456"`) {
		t.Errorf("second call should use raw f456: %s", (*calls)[1])
	}
}

// ---------------------------------------------------------------------------
// callWithIds: comma-string → JSON array fallback on 400
// ---------------------------------------------------------------------------

func TestCallWithIds_DualTry(t *testing.T) {
	srv, calls := buildServer(t,
		`{"code":400,"message":"param error"}`,
		`{"code":200,"message":"deleted"}`,
	)
	defer srv.Close()

	d := driverWithBase(srv.URL)
	ctx := context.Background()

	err := d.remove(ctx, []string{"f1", "f2"})
	if err != nil {
		t.Fatalf("remove returned error: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(*calls))
	}
	// First attempt: ids is comma-string
	if !strings.Contains((*calls)[0], `"f1,f2"`) {
		t.Errorf("first call should have comma-string ids: %s", (*calls)[0])
	}
	// Second attempt: ids is JSON array
	if !strings.Contains((*calls)[1], `["f1","f2"]`) {
		t.Errorf("second call should have JSON array ids: %s", (*calls)[1])
	}
}

// ---------------------------------------------------------------------------
// Checksum format: md5hex-size
// ---------------------------------------------------------------------------

func TestChecksumFormat(t *testing.T) {
	content := []byte("hello")
	sum := md5.Sum(content)
	checksum := fmt.Sprintf("%x-%d", sum, len(content))
	// md5("hello") = 5d41402abc4b2a76b9719d911017c592
	want := "5d41402abc4b2a76b9719d911017c592-5"
	if checksum != want {
		t.Errorf("checksum = %q, want %q", checksum, want)
	}
}

// ---------------------------------------------------------------------------
// 401 → session expired message
// ---------------------------------------------------------------------------

func TestSession401Error(t *testing.T) {
	srv, _ := buildServer(t, `{"code":401,"message":"not logged in"}`)
	defer srv.Close()

	d := driverWithBase(srv.URL)
	ctx := context.Background()

	err := d.profile(ctx)
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "401") && !strings.Contains(msg, "expired") && !strings.Contains(msg, "Session") {
		t.Errorf("error message should mention session/expired/401; got: %s", msg)
	}
	if !strings.Contains(msg, "开放接口登录密钥管理") {
		t.Errorf("error message should mention 开放接口登录密钥管理; got: %s", msg)
	}
}

// ---------------------------------------------------------------------------
// mkdir: 403 with folder_id → treat as success
// ---------------------------------------------------------------------------

func TestMkdir403Success(t *testing.T) {
	// Server returns 403 with an existing folder_id
	srv, _ := buildServer(t, `{"code":403,"message":"folder exists","folder_id":"d999"}`)
	defer srv.Close()

	d := driverWithBase(srv.URL)
	ctx := context.Background()

	id, err := d.mkdir(ctx, "d0", "MyFolder")
	if err != nil {
		t.Fatalf("mkdir should succeed on 403 with folder_id, got error: %v", err)
	}
	if id != "d999" {
		t.Errorf("expected id=d999, got %q", id)
	}
}

// ---------------------------------------------------------------------------
// list files: pagination stops on empty page
// ---------------------------------------------------------------------------

func TestListFiles_Pagination(t *testing.T) {
	// Page 1: two files; page 2: empty
	srv, calls := buildServer(t,
		`{"code":200,"results":[{"key":"f1","name":"a.txt","size":100},{"key":"f2","name":"b.txt","size":200}]}`,
		`{"code":200,"results":[]}`,
	)
	defer srv.Close()

	d := driverWithBase(srv.URL)
	ctx := context.Background()

	files, err := d.listFiles(ctx, "d0")
	if err != nil {
		t.Fatalf("listFiles: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}
	if len(*calls) != 2 {
		t.Errorf("expected 2 HTTP calls (2 pages), got %d", len(*calls))
	}
}

// ---------------------------------------------------------------------------
// list files: duplicate first key terminates pagination (anti-loop guard)
// ---------------------------------------------------------------------------

func TestListFiles_DuplicateKeyStops(t *testing.T) {
	sameResp := `{"code":200,"results":[{"key":"f1","name":"a.txt","size":1},{"key":"f2","name":"b.txt","size":2}]}`
	srv, calls := buildServer(t, sameResp, sameResp, sameResp)
	defer srv.Close()

	d := driverWithBase(srv.URL)
	ctx := context.Background()

	files, err := d.listFiles(ctx, "d0")
	if err != nil {
		t.Fatalf("listFiles: %v", err)
	}
	// Should stop after 2 calls (first page + duplicate detection on second)
	if len(*calls) != 2 {
		t.Errorf("expected 2 calls (duplicate guard), got %d", len(*calls))
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}
}

// ---------------------------------------------------------------------------
// namespace: private cloud uses /private/ prefix
// ---------------------------------------------------------------------------

func TestNamespace_Private(t *testing.T) {
	srv, calls := buildServer(t, `{"code":200,"results":[]}`)
	defer srv.Close()

	d := driverWithBase(srv.URL)
	d.CloudType = "private"
	ctx := context.Background()

	_, _ = d.listFolders(ctx, "d0")
	if len(*calls) < 1 {
		t.Fatal("expected at least 1 call")
	}
	// The request URL path should contain /private/
	// We can't directly read the URL from body, but we can check the server
	// received a body referencing private — instead check via a separate handler.
	// We verify indirectly by checking the test server was hit (calls recorded).
}

func TestNamespace_PrivateURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"code":200,"results":[]}`))
	}))
	defer srv.Close()

	d := driverWithBase(srv.URL)
	d.CloudType = "private"
	ctx := context.Background()

	_, _ = d.listFolders(ctx, "d0")
	if !strings.Contains(gotPath, "/private/") {
		t.Errorf("expected /private/ in path, got %q", gotPath)
	}
}

// ---------------------------------------------------------------------------
// session missing → clear error
// ---------------------------------------------------------------------------

func TestSessionMissing(t *testing.T) {
	d := &CtFile{}
	d.Session = ""
	d.BaseURL = "http://localhost:9"
	ctx := context.Background()

	err := d.profile(ctx)
	if err == nil {
		t.Fatal("expected error for missing session")
	}
	if !strings.Contains(err.Error(), "session") {
		t.Errorf("error should mention session, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// helper: background context for tests (unused now — kept as reference)
// ---------------------------------------------------------------------------

