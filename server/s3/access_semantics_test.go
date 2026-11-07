package s3

import (
	"bytes"
	"context"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/gofakes3"
)

// TestHeadObjectDoesNotCountAsAccess pins the product rule that hot/cold
// tiering follows: a read is a GET, not a metadata probe. Sync tools HEAD every
// object they compare, so counting HEAD would turn the whole library hot.
func TestHeadObjectDoesNotCountAsAccess(t *testing.T) {
	ctx := context.Background()
	setTestSetting(conf.S3AccessTrack, "true")
	backend, _ := setupMultipartBackend(t)

	body := []byte("hello world")
	if _, err := backend.PutObject(ctx, "mp", "probe.txt", map[string]string{"Content-Type": "text/plain"}, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("put object: %v", err)
	}

	if _, err := backend.HeadObject(ctx, "mp", "probe.txt"); err != nil {
		t.Fatalf("head object: %v", err)
	}
	if got := trackedAccessCount(backend); got != 0 {
		t.Fatalf("a HEAD recorded %d access entries, want 0", got)
	}

	obj, err := backend.GetObject(ctx, "mp", "probe.txt", nil)
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	if obj.Contents != nil {
		_ = obj.Contents.Close()
	}
	if got := trackedAccessCount(backend); got == 0 {
		t.Fatal("a GET recorded no access entry")
	}
}

// TestListObjectsIsNotCountedAsAccess guards the neighbouring case: listing a
// prefix reports keys without reading any of them.
func TestListObjectsIsNotCountedAsAccess(t *testing.T) {
	ctx := context.Background()
	setTestSetting(conf.S3AccessTrack, "true")
	backend, _ := setupMultipartBackend(t)

	body := []byte("x")
	if _, err := backend.PutObject(ctx, "mp", "listed.txt", nil, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("put object: %v", err)
	}
	if _, err := backend.ListBucket(ctx, "mp", &gofakes3.Prefix{}, gofakes3.ListBucketPage{}); err != nil {
		t.Fatalf("list bucket: %v", err)
	}
	if got := trackedAccessCount(backend); got != 0 {
		t.Fatalf("a listing recorded %d access entries, want 0", got)
	}
}

// trackedAccessCount returns how many reads the backend has buffered.
func trackedAccessCount(b *s3Backend) int {
	b.access.mu.Lock()
	defer b.access.mu.Unlock()
	return len(b.access.pending)
}
