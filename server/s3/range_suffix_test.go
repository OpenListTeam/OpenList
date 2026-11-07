package s3

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/gofakes3"
)

// suffixRangeChain builds the middleware chain NewServer builds over a Local
// storage mounted at a per-test mount path, with one writable signed bucket.
func suffixRangeChain(t *testing.T) (http.Handler, string) {
	t.Helper()
	setupMultipartBackend(t) // creates the shared in-memory database
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mount := "/suffix-" + sanitizeTestName(t.Name())
	root, err := os.MkdirTemp("", "openlist-s3-suffix-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	addition, err := json.Marshal(struct {
		RootFolderPath string `json:"root_folder_path"`
		Thumbnail      bool   `json:"thumbnail"`
	}{RootFolderPath: root})
	if err != nil {
		t.Fatalf("marshal local addition: %v", err)
	}
	if _, err := op.CreateStorage(ctx, model.Storage{
		Driver:    "Local",
		MountPath: mount,
		Addition:  string(addition),
	}); err != nil {
		t.Fatalf("create storage: %v", err)
	}
	setTestSetting(conf.S3Buckets, `[{"name":"sr","path":"`+mount+`"}]`)
	setTestSetting(conf.S3BucketAuth, `[{"access_key":"sr-key","secret_key":"sr-secret","buckets":["sr"],"readonly":false}]`)
	t.Cleanup(func() {
		setTestSetting(conf.S3Buckets, "[]")
		setTestSetting(conf.S3BucketAuth, "[]")
	})

	handler, err := NewServer(ctx)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return handler, root
}

func signedGet(t *testing.T, handler http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	signV2Request(t, req, "sr-key", "sr-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// A suffix range that asks for more bytes than the object holds selects the
// whole representation (RFC 7233 §2.1: "if the selected representation is
// shorter than the specified suffix-length, the entire representation is
// used"). gofakes3's ObjectRangeRequest.Range turns that case into
// ErrInvalidRange, which the gateway answered with 416 InvalidRange, so every
// tail read larger than the stored file failed instead of returning the file:
// `curl -r -500`, `aws s3api get-object --range bytes=-500`, `tail -c`-style
// resumers and players that ask for a window bigger than a small clip all got
// an error where S3 returns the bytes.
func TestSuffixRangeLongerThanObjectServesWholeObject(t *testing.T) {
	handler, root := suffixRangeChain(t)
	const body = "0123456789" // 10 bytes
	if err := os.WriteFile(filepath.Join(root, "small.bin"), []byte(body), 0o644); err != nil {
		t.Fatalf("write object: %v", err)
	}

	cases := []struct {
		rangeHeader string
		status      int
		contentRange,
		body string
	}{
		// Longer than the object: the whole representation, as a 206.
		{rangeHeader: "bytes=-11", status: http.StatusPartialContent, contentRange: "bytes 0-9/10", body: body},
		{rangeHeader: "bytes=-500", status: http.StatusPartialContent, contentRange: "bytes 0-9/10", body: body},
		// Exactly the object size still works the normal way.
		{rangeHeader: "bytes=-10", status: http.StatusPartialContent, contentRange: "bytes 0-9/10", body: body},
		// A suffix that fits must stay a real tail read.
		{rangeHeader: "bytes=-5", status: http.StatusPartialContent, contentRange: "bytes 5-9/10", body: "56789"},
		{rangeHeader: "bytes=-1", status: http.StatusPartialContent, contentRange: "bytes 9-9/10", body: "9"},
		// A zero-length suffix names no byte at all and stays unsatisfiable.
		{rangeHeader: "bytes=-0", status: http.StatusRequestedRangeNotSatisfiable},
	}
	for _, tc := range cases {
		t.Run(tc.rangeHeader, func(t *testing.T) {
			rec := signedGet(t, handler, "/sr/small.bin", map[string]string{"Range": tc.rangeHeader})
			got, _ := io.ReadAll(rec.Body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.status, got)
			}
			if tc.status != http.StatusPartialContent {
				return
			}
			if cr := rec.Header().Get("Content-Range"); cr != tc.contentRange {
				t.Fatalf("Content-Range = %q, want %q", cr, tc.contentRange)
			}
			if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(tc.body)) {
				t.Fatalf("Content-Length = %q, want %d", cl, len(tc.body))
			}
			if string(got) != tc.body {
				t.Fatalf("body = %q, want %q", got, tc.body)
			}
		})
	}
}

// An oversize suffix on an empty object has no byte to select: it stays a range
// error rather than turning into a zero-length 206 with a bogus Content-Range.
func TestSuffixRangeOnEmptyObjectStaysUnsatisfiable(t *testing.T) {
	handler, root := suffixRangeChain(t)
	if err := os.WriteFile(filepath.Join(root, "zero.bin"), nil, 0o644); err != nil {
		t.Fatalf("write object: %v", err)
	}
	rec := signedGet(t, handler, "/sr/zero.bin", map[string]string{"Range": "bytes=-5"})
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", rec.Code)
	}
}

// The backend decides the range, so it is the layer that has to widen an
// oversize suffix: gofakes3 converts whatever comes back into Content-Range and
// Content-Length without a second look.
func TestGetObjectSuffixRangeBeyondSizeWidens(t *testing.T) {
	backend, _ := setupMultipartBackend(t)
	const bucket = "mp"
	body := []byte("0123456789")
	writeTestObject(t, backend, bucket, "tail.bin", body)

	for _, suffix := range []int64{10, 11, 500} {
		obj, err := backend.GetObject(context.Background(), bucket, "tail.bin",
			&gofakes3.ObjectRangeRequest{FromEnd: true, End: suffix})
		if err != nil {
			t.Fatalf("suffix %d: GetObject: %+v", suffix, err)
		}
		if obj.Range == nil {
			_ = obj.Contents.Close()
			t.Fatalf("suffix %d: range was dropped instead of selecting the whole object", suffix)
		}
		rng := *obj.Range
		got, readErr := io.ReadAll(obj.Contents)
		_ = obj.Contents.Close()
		if readErr != nil {
			t.Fatalf("suffix %d: read: %+v", suffix, readErr)
		}
		if rng.Start != 0 || rng.Length != int64(len(body)) {
			t.Fatalf("suffix %d: range = %+v, want start 0 length %d", suffix, rng, len(body))
		}
		if string(got) != string(body) {
			t.Fatalf("suffix %d: body = %q, want %q", suffix, got, body)
		}
	}
}

func TestSignedMultiRangeServesWholeObject(t *testing.T) {
	handler, root := suffixRangeChain(t)
	const body = "0123456789abcdef"
	if err := os.WriteFile(filepath.Join(root, "multi.bin"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/sr/multi.bin", nil)
	req.Header.Set("Range", "bytes=0-2,5-6")
	signV4Header(t, req, "sr-key", "sr-secret", []string{"host", "range", "x-amz-content-sha256", "x-amz-date"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != body {
		t.Fatalf("status=%d body=%q headers=%v", rec.Code, rec.Body.String(), rec.Header())
	}
}
