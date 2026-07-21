package s3

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"

	"github.com/OpenListTeam/gofakes3"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func init() {
	dataDir, err := os.MkdirTemp("", "openlist-s3-mp-*")
	if err != nil {
		panic(err)
	}
	conf.Conf = conf.DefaultConfig(dataDir)
	if err := os.MkdirAll(conf.Conf.TempDir, 0o755); err != nil {
		panic("mkdir temp dir: " + err.Error())
	}
	dB, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		panic("failed to connect database: " + err.Error())
	}
	db.Init(dB)
}

// s3ErrorCode extracts the gofakes3 ErrorCode from an error returned by the
// MultipartBackend methods.
func s3ErrorCode(err error) gofakes3.ErrorCode {
	if err == nil {
		return gofakes3.ErrNone
	}
	var s3err interface{ ErrorCode() gofakes3.ErrorCode }
	if errors.As(err, &s3err) {
		return s3err.ErrorCode()
	}
	return gofakes3.ErrNone
}

// setupMultipartBackend prepares a Local storage mounted at /mpbucket and an
// s3Backend with an "mp" bucket pointing at it. It returns the backend, the
// local root directory on disk, and a cleanup function.
func setupMultipartBackend(t *testing.T) (*s3Backend, string) {
	t.Helper()
	ctx := context.Background()

	// Unique mount path and bucket per test: the in-memory sqlite is shared
	// across tests in this package, so a fixed mount path would clash.
	mount := "/" + sanitizeTestName(t.Name())
	bucket := "mp"

	localRoot, err := os.MkdirTemp("", "openlist-s3-local-*")
	if err != nil {
		t.Fatalf("mkdir local root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(localRoot) })

	_, err = op.CreateStorage(ctx, model.Storage{
		Driver:    "Local",
		MountPath: mount,
		Addition:  `{"root_folder_path":"` + localRoot + `","thumbnail":false}`,
	})
	if err != nil {
		t.Fatalf("create local storage: %+v", err)
	}

	if err := op.SaveSettingItem(&model.SettingItem{
		Key:   conf.S3Buckets,
		Value: `[{"name":"` + bucket + `","path":"` + mount + `"}]`,
	}); err != nil {
		t.Fatalf("save s3 buckets setting: %+v", err)
	}

	return newBackend().(*s3Backend), localRoot
}

func sanitizeTestName(name string) string {
	r := strings.NewReplacer("/", "_", " ", "_")
	return r.Replace(name)
}

func TestMultipartUploadEndToEnd(t *testing.T) {
	ctx := context.Background()
	b, localRoot := setupMultipartBackend(t)

	meta := map[string]string{"Content-Type": "text/plain"}
	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "dir/hello.txt", meta)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}
	if uploadID == "" {
		t.Fatal("empty upload id")
	}

	part := func(n int, body string) string {
		t.Helper()
		etag, err := b.UploadPart(ctx, "mp", "dir/hello.txt", uploadID, n, int64(len(body)), strings.NewReader(body))
		if err != nil {
			t.Fatalf("UploadPart %d: %+v", n, err)
		}
		return etag
	}

	etag1 := part(1, "Hello, ")
	etag2 := part(2, "multipart ")
	etag3 := part(3, "world!")

	// Re-uploading the same part number overwrites it and returns a fresh etag.
	if e := part(2, "multipart "); e != etag2 {
		t.Fatalf("re-upload part 2 etag = %q, want %q", e, etag2)
	}

	// Short read (body smaller than declared Content-Length) must fail.
	_, shortErr := b.UploadPart(ctx, "mp", "dir/hello.txt", uploadID, 4, 10, strings.NewReader("abc"))
	shortErr = s3ErrorCode(shortErr)
	if shortErr != gofakes3.ErrIncompleteBody {
		t.Fatalf("short read error = %v, want IncompleteBody", shortErr)
	}

	// Unknown upload id.
	_, uerr := b.UploadPart(ctx, "mp", "dir/hello.txt", "does-not-exist", 1, 1, strings.NewReader("x"))
	if code := s3ErrorCode(uerr); code != gofakes3.ErrNoSuchUpload {
		t.Fatalf("unknown upload error = %v, want NoSuchUpload", code)
	}
	// Out-of-range part number.
	_, perr := b.UploadPart(ctx, "mp", "dir/hello.txt", uploadID, 0, 1, strings.NewReader("x"))
	if code := s3ErrorCode(perr); code != gofakes3.ErrInvalidPart {
		t.Fatalf("part 0 error = %v, want InvalidPart", code)
	}

	// Parts out of order.
	_, _, err = b.CompleteMultipartUpload(ctx, "mp", "dir/hello.txt", uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{
			{PartNumber: 2, ETag: etag2},
			{PartNumber: 1, ETag: etag1},
		},
	})
	if code := s3ErrorCode(err); code != gofakes3.ErrInvalidPartOrder {
		t.Fatalf("out-of-order complete error = %v, want InvalidPartOrder", code)
	}

	// Wrong etag.
	_, _, err = b.CompleteMultipartUpload(ctx, "mp", "dir/hello.txt", uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{
			{PartNumber: 1, ETag: etag1},
			{PartNumber: 2, ETag: `"deadbeef"`},
			{PartNumber: 3, ETag: etag3},
		},
	})
	if code := s3ErrorCode(err); code != gofakes3.ErrInvalidPart {
		t.Fatalf("wrong etag complete error = %v, want InvalidPart", code)
	}

	// Missing part number.
	_, _, err = b.CompleteMultipartUpload(ctx, "mp", "dir/hello.txt", uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{
			{PartNumber: 1, ETag: etag1},
			{PartNumber: 99, ETag: etag3},
		},
	})
	if code := s3ErrorCode(err); code != gofakes3.ErrInvalidPart {
		t.Fatalf("missing part complete error = %v, want InvalidPart", code)
	}

	// The failed completes must leave the upload available for retry.
	if _, ok := b.uploads.Load(uploadID); !ok {
		t.Fatal("upload was removed after a failed complete")
	}

	// Successful complete.
	_, etag, err := b.CompleteMultipartUpload(ctx, "mp", "dir/hello.txt", uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{
			{PartNumber: 1, ETag: etag1},
			{PartNumber: 2, ETag: etag2},
			{PartNumber: 3, ETag: etag3},
		},
	})
	if err != nil {
		t.Fatalf("CompleteMultipartUpload: %+v", err)
	}
	if !strings.HasSuffix(etag, `-3"`) || !strings.HasPrefix(etag, `"`) {
		t.Fatalf("complete etag = %q, want a quoted \"<hex>-3\" multipart etag", etag)
	}

	// The object must exist on disk with the concatenated content.
	got, err := os.ReadFile(filepath.Join(localRoot, "dir", "hello.txt"))
	if err != nil {
		t.Fatalf("read resulting file: %+v", err)
	}
	want := []byte("Hello, multipart world!")
	if !bytes.Equal(got, want) {
		t.Fatalf("resulting file content = %q, want %q", got, want)
	}

	// Bookkeeping and temp files must be cleaned up on success.
	if _, ok := b.uploads.Load(uploadID); ok {
		t.Fatal("upload still tracked after successful complete")
	}
}

func TestMultipartAbort(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "abort.txt", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}
	if _, err := b.UploadPart(ctx, "mp", "abort.txt", uploadID, 1, 3, strings.NewReader("abc")); err != nil {
		t.Fatalf("UploadPart: %+v", err)
	}

	state, _ := b.uploads.Load(uploadID)
	dir := state.(*multipartState).dir
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("temp dir missing before abort: %+v", err)
	}

	if err := b.AbortMultipartUpload(ctx, "mp", "abort.txt", uploadID); err != nil {
		t.Fatalf("AbortMultipartUpload: %+v", err)
	}
	if _, ok := b.uploads.Load(uploadID); ok {
		t.Fatal("upload still tracked after abort")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temp dir still exists after abort (err=%v)", err)
	}

	// Aborting an unknown upload must be idempotent.
	if err := b.AbortMultipartUpload(ctx, "mp", "abort.txt", "nope"); err != nil {
		t.Fatalf("abort unknown upload returned error: %+v", err)
	}
}

func TestMultipartReapExpired(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	// An active upload (fresh lastActivity) must be kept.
	freshID, err := b.CreateMultipartUpload(ctx, "mp", "fresh.txt", nil)
	if err != nil {
		t.Fatalf("create fresh upload: %+v", err)
	}

	// An abandoned upload (stale lastActivity) must be reaped.
	staleID, err := b.CreateMultipartUpload(ctx, "mp", "stale.txt", nil)
	if err != nil {
		t.Fatalf("create stale upload: %+v", err)
	}
	if _, err := b.UploadPart(ctx, "mp", "stale.txt", staleID, 1, 3, strings.NewReader("abc")); err != nil {
		t.Fatalf("upload stale part: %+v", err)
	}
	staleState, _ := b.uploads.Load(staleID)
	staleDir := staleState.(*multipartState).dir
	if _, err := os.Stat(staleDir); err != nil {
		t.Fatalf("stale temp dir missing: %+v", err)
	}

	// Force the stale upload's lastActivity well into the past.
	ttl := 30 * time.Minute
	now := time.Now()
	staleState.(*multipartState).mu.Lock()
	staleState.(*multipartState).lastActivity = now.Add(-2 * ttl)
	staleState.(*multipartState).mu.Unlock()

	b.reapExpired(now, ttl)

	if _, ok := b.uploads.Load(staleID); ok {
		t.Fatal("stale upload still tracked after reap")
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale temp dir still exists after reap (err=%v)", err)
	}
	if _, ok := b.uploads.Load(freshID); !ok {
		t.Fatal("fresh upload was reaped, should have been kept")
	}
}

func TestMultipartCleanupStaleDirs(t *testing.T) {
	b, _ := setupMultipartBackend(t)

	tempDir := conf.Conf.TempDir
	staleDir, err := os.MkdirTemp(tempDir, multipartDirPrefix+"*")
	if err != nil {
		t.Fatalf("mkdir stale dir: %v", err)
	}
	freshDir, err := os.MkdirTemp(tempDir, multipartDirPrefix+"*")
	if err != nil {
		t.Fatalf("mkdir fresh dir: %v", err)
	}
	// Age the stale dir beyond the TTL; leave the fresh dir young.
	ttl := 30 * time.Minute
	now := time.Now()
	past := now.Add(-2 * ttl)
	if err := os.Chtimes(staleDir, past, past); err != nil {
		t.Fatalf("chtimes stale dir: %v", err)
	}

	b.cleanupStaleDirs(now, ttl)

	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale dir should have been removed (err=%v)", err)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("fresh dir should have been kept (err=%v)", err)
	}
	_ = os.RemoveAll(freshDir)
}
