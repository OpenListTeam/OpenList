package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"

	"github.com/OpenListTeam/gofakes3"
)

// useEtagMode selects the ETag scheme for one test.
func useEtagMode(t *testing.T, mode string) {
	t.Helper()
	if err := op.SaveSettingItem(&model.SettingItem{Key: conf.S3EtagMode, Value: mode}); err != nil {
		t.Fatalf("save s3_etag_mode: %v", err)
	}
	t.Cleanup(func() {
		if err := op.SaveSettingItem(&model.SettingItem{Key: conf.S3EtagMode, Value: etagModeContent}); err != nil {
			t.Errorf("restore s3_etag_mode: %v", err)
		}
	})
}

// etagOf reports the ETag of an object the way a client receives it: either the
// value the backend returned explicitly or the hash it is built from.
func etagOf(t *testing.T, obj *gofakes3.Object) string {
	t.Helper()
	if obj.ETag != "" {
		return strings.Trim(obj.ETag, `"`)
	}
	return hex.EncodeToString(obj.Hash)
}

func md5Hex(content string) string {
	sum := md5.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}

// An object written through the S3 server has its ETag on record, so a restart
// does not make the server read it back to answer a HEAD.
func TestVersionModeRecordsEtagOnUpload(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeVersion)
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	const body = "0123456789"
	if _, err := b.PutObject(ctx, "mp", "seg.bin", map[string]string{"Content-Type": "text/plain"}, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("PutObject: %+v", err)
	}

	obj, err := b.HeadObject(ctx, "mp", "seg.bin")
	if err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}
	if got, want := etagOf(t, obj), md5Hex(body); got != want {
		t.Fatalf("ETag after upload = %s, want the content MD5 %s", got, want)
	}

	row, ok := loadStoredRow(ctx, filepath.Join("/"+sanitizeTestName(t.Name()), "seg.bin"))
	if !ok || row.ETag != md5Hex(body) {
		t.Fatalf("recorded ETag = %+v (found=%v), want %s", row, ok, md5Hex(body))
	}
}

// After a restart the recorded ETag is all that is left, and it has to be the
// same one the client saw when it uploaded.
func TestVersionModeKeepsEtagAcrossRestart(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeVersion)
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	const body = "restart me"
	if _, err := b.PutObject(ctx, "mp", "keep.bin", map[string]string{"Content-Type": "text/plain"}, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("PutObject: %+v", err)
	}
	before, err := b.HeadObject(ctx, "mp", "keep.bin")
	if err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}

	b.meta = new(sync.Map) // a new process knows nothing in memory

	after, err := b.HeadObject(ctx, "mp", "keep.bin")
	if err != nil {
		t.Fatalf("HeadObject after restart: %+v", err)
	}
	if got, want := etagOf(t, after), etagOf(t, before); got != want {
		t.Fatalf("ETag changed across a restart: %s -> %s", want, got)
	}
}

// An object that was never written through the server gets an ETag derived from
// its key, size and modification time, and that answer is kept: the alternative
// is a full download on every HEAD and on every range read.
func TestVersionModeDerivesEtagOnceForUnknownObject(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeVersion)
	ctx := context.Background()
	b, root := setupMultipartBackend(t)

	const body = "written behind the server's back"
	name := filepath.Join(root, "orphan.bin")
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatalf("write orphan: %v", err)
	}

	first, err := b.HeadObject(ctx, "mp", "orphan.bin")
	if err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}
	key := filepath.Join("/"+sanitizeTestName(t.Name()), "orphan.bin")
	if row, ok := loadStoredRow(ctx, key); !ok || row.ETag != etagOf(t, first) {
		t.Fatalf("derived ETag was not recorded: %+v (found=%v)", row, ok)
	}

	b.meta = new(sync.Map)

	second, err := b.HeadObject(ctx, "mp", "orphan.bin")
	if err != nil {
		t.Fatalf("HeadObject after restart: %+v", err)
	}
	if got, want := etagOf(t, second), etagOf(t, first); got != want {
		t.Fatalf("derived ETag changed across a restart: %s -> %s", want, got)
	}
	if got := etagOf(t, second); got == md5Hex(body) {
		t.Fatal("version mode fell back to hashing the content")
	}
}

// A conditional write needs the ETag to change when the object changes, which is
// what makes the derived value usable for a block device.
func TestVersionModeEtagFollowsContentChanges(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeVersion)
	ctx := context.Background()
	b, root := setupMultipartBackend(t)

	name := filepath.Join(root, "changing.bin")
	if err := os.WriteFile(name, []byte("first"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	first, err := b.HeadObject(ctx, "mp", "changing.bin")
	if err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}

	if err := os.WriteFile(name, []byte("second, longer"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	second, err := b.HeadObject(ctx, "mp", "changing.bin")
	if err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}

	if etagOf(t, first) == etagOf(t, second) {
		t.Fatalf("ETag did not change with the content: %s", etagOf(t, first))
	}
}

// The default scheme is unchanged: the ETag is the MD5 of the content, whatever
// the object's history is.
func TestContentModeHashesUnknownObject(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeContent)
	ctx := context.Background()
	b, root := setupMultipartBackend(t)

	const body = "hash me"
	if err := os.WriteFile(filepath.Join(root, "plain.bin"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	obj, err := b.HeadObject(ctx, "mp", "plain.bin")
	if err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}
	if got, want := etagOf(t, obj), md5Hex(body); got != want {
		t.Fatalf("ETag = %s, want the content MD5 %s", got, want)
	}
}

// A second upload has to replace the recorded ETag. It is the path that updates
// an existing row instead of inserting one, and the one that leaves a stale ETag
// behind when the column names in the update drift from the model.
func TestVersionModeUpdatesRecordedEtagOnOverwrite(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeVersion)
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	for _, body := range []string{"first", "second, longer"} {
		if _, err := b.PutObject(ctx, "mp", "again.bin", map[string]string{"Content-Type": "text/plain"}, strings.NewReader(body), int64(len(body))); err != nil {
			t.Fatalf("PutObject(%q): %+v", body, err)
		}
	}

	key := filepath.Join("/"+sanitizeTestName(t.Name()), "again.bin")
	row, ok := loadStoredRow(ctx, key)
	if !ok {
		t.Fatal("no row recorded for the object")
	}
	if want := md5Hex("second, longer"); row.ETag != want {
		t.Fatalf("recorded ETag = %s, want %s", row.ETag, want)
	}
	if want := int64(len("second, longer")); row.Size != want {
		t.Fatalf("recorded size = %d, want %d", row.Size, want)
	}

	obj, err := b.HeadObject(ctx, "mp", "again.bin")
	if err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}
	if got, want := etagOf(t, obj), md5Hex("second, longer"); got != want {
		t.Fatalf("HEAD ETag = %s, want %s", got, want)
	}
}

// An unrecognised value must resolve to the default scheme, not to the one that
// has to read whole objects.
func TestUnknownEtagModeFallsBackToVersion(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, "generation")
	if got := currentEtagMode(); got != etagModeVersion {
		t.Fatalf("unknown mode resolved to %q, want %q", got, etagModeVersion)
	}
}

// TestRecordIdentityConcurrentUpsert verifies that multiple goroutines recording
// identity for the same key concurrently do not cause duplicate key errors.
func TestRecordIdentityConcurrentUpsert(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()
	b := &s3Backend{}

	const workers = 10
	const key = "/bucket/test-concurrent-etag.bin"
	var wg sync.WaitGroup
	wg.Add(workers)

	now := time.Now().Unix()
	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			b.recordIdentity(ctx, key, "etag-123", 1024, now)
		}(i)
	}
	wg.Wait()

	row, ok := loadStoredRow(ctx, key)
	if !ok {
		t.Fatalf("expected stored row for %s, got none", key)
	}
	if row.ETag != "etag-123" || row.Size != 1024 || row.ModTime != now {
		t.Fatalf("row mismatch: %+v", row)
	}
}
