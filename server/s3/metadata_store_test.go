package s3

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// TestMain gives the package a real database, because the metadata of an
// object outlives the process and can only be tested against one.
func TestMain(m *testing.M) {
	database, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		panic(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		panic(err)
	}
	// A shared in-memory database only exists while a connection is open, and
	// one connection keeps every test on the same copy of it.
	sqlDB.SetMaxOpenConns(1)
	db.Init(database)
	code := m.Run()
	// init() of this package creates a data directory that holds every part
	// file the multipart tests write; nothing else ever removes it, so without
	// this a test run leaves it (and its part files) in the system temp dir.
	if multipartTestDataDir != "" {
		_ = os.RemoveAll(multipartTestDataDir)
	}
	os.Exit(code)
}

// resetMetadataTable clears persisted metadata so tests stay independent.
func resetMetadataTable(t *testing.T) {
	t.Helper()
	database := db.GetDb()
	if database == nil {
		t.Fatal("test database is not initialized")
	}
	if err := database.Where("1 = 1").Delete(&model.S3ObjectMetadata{}).Error; err != nil {
		t.Fatalf("reset s3_object_metadata: %v", err)
	}
}

func countMetadataRows(t *testing.T) int64 {
	t.Helper()
	var count int64
	if err := db.GetDb().Model(&model.S3ObjectMetadata{}).Count(&count).Error; err != nil {
		t.Fatalf("count s3_object_metadata: %v", err)
	}
	return count
}

// A restart drops the in-memory map, so anything that has to survive one must
// come back from the database.
func TestMetadataSurvivesProcessRestart(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()
	headers := map[string]string{"Content-Type": "text/plain", "X-Amz-Meta-Pin": "deadbeef"}

	before := &s3Backend{meta: new(sync.Map)}
	before.storeTestMetadata(ctx, "/bucket/meta.txt", headers)

	after := &s3Backend{meta: new(sync.Map)}
	stored, ok := after.loadTestMetadata(ctx, "/bucket/meta.txt")
	if !ok || stored["x-amz-meta-pin"] != "deadbeef" {
		t.Fatalf("metadata lost across a restart: %v", stored)
	}
}

// The common case must not pay for a row.
func TestPlainUploadCreatesNoRow(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeTestMetadata(ctx, "/bucket/plain.txt", map[string]string{"Content-Type": "text/plain"})

	if rows := countMetadataRows(t); rows != 0 {
		t.Fatalf("a plain upload left %d row(s) behind", rows)
	}
	if _, ok := b.loadStoredMetadata(ctx, "/bucket/plain.txt"); ok {
		t.Fatal("plain upload reported persisted metadata")
	}
}

// S3 replaces metadata on every upload, so an overwrite that carries none must
// not let the previous metadata describe the new content.
func TestOverwriteWithoutUserMetadataDropsStaleRow(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeTestMetadata(ctx, "/bucket/m.txt", map[string]string{"X-Amz-Meta-Pin": "old"})
	b.storeTestMetadata(ctx, "/bucket/m.txt", map[string]string{"Content-Type": "text/plain"})

	if _, ok := b.loadStoredMetadata(ctx, "/bucket/m.txt"); ok {
		t.Fatal("stale metadata survived an overwrite")
	}
	// The row itself may stay behind to carry the ETag of the object, but it must
	// no longer describe user metadata.
	if stored, ok := b.loadTestMetadata(ctx, "/bucket/m.txt"); ok && hasUserMetadata(stored) {
		t.Fatalf("overwrite kept user metadata: %v", stored)
	}
}

func TestDeleteRemovesPersistedMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeTestMetadata(ctx, "/bucket/gone.txt", map[string]string{"X-Amz-Meta-Pin": "deadbeef"})
	b.meta.Delete("/bucket/gone.txt")
	b.deleteStoredMetadata(ctx, "/bucket/gone.txt")

	if _, ok := b.loadTestMetadata(ctx, "/bucket/gone.txt"); ok {
		t.Fatal("metadata survived deletion")
	}
}

func TestCopyCarriesMetadataToDestination(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeTestMetadata(ctx, "/bucket/src.txt", map[string]string{"X-Amz-Meta-Pin": "carried"})
	// The destination upload itself carries no user metadata, which is what a
	// COPY directive does before the recorded copy is applied.
	b.storeTestMetadata(ctx, "/bucket/dst.txt", map[string]string{"Content-Type": "text/plain"})
	b.copyStoredMetadata(ctx, "/bucket/src.txt", "/bucket/dst.txt")

	stored, ok := b.loadStoredMetadata(ctx, "/bucket/dst.txt")
	if !ok || stored["x-amz-meta-pin"] != "carried" {
		t.Fatalf("copy did not carry metadata: %v", stored)
	}
	if source, ok := b.loadStoredMetadata(ctx, "/bucket/src.txt"); !ok || source["x-amz-meta-pin"] != "carried" {
		t.Fatalf("copy damaged the source metadata: %v", source)
	}
}

// The unique index uses a hash, so the path itself may be arbitrarily long.
func TestLongPathMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()
	name := "/bucket/" + longTestKey(300)

	b := &s3Backend{meta: new(sync.Map)}
	b.storeTestMetadata(ctx, name, map[string]string{"X-Amz-Meta-Pin": "deadbeef"})

	if stored, ok := b.loadStoredMetadata(ctx, name); !ok || stored["x-amz-meta-pin"] != "deadbeef" {
		t.Fatalf("metadata lost for long path: %v", stored)
	}
}

func TestMetadataDirectiveDecidesWhetherMetadataIsReplaced(t *testing.T) {
	if got := metadataDirective(map[string]string{"X-Amz-Metadata-Directive": "REPLACE"}); got != "REPLACE" {
		t.Fatalf("canonical header not recognised: %q", got)
	}
	if got := metadataDirective(map[string]string{"x-amz-metadata-directive": "replace"}); got != "replace" {
		t.Fatalf("lowercase header not recognised: %q", got)
	}
	if got := metadataDirective(map[string]string{"Content-Type": "text/plain"}); got != "" {
		t.Fatalf("absent directive reported as %q", got)
	}
}

// A self-copy with REPLACE and no metadata is the documented way to strip an
// object's metadata without rewriting its content.
func TestSelfCopyReplaceWithoutMetadataClearsMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeTestMetadata(ctx, "/bucket/clear.txt", map[string]string{"X-Amz-Meta-Pin": "deadbeef"})

	if _, err := b.replaceObjectMetadata(ctx, "/bucket/clear.txt", map[string]string{"X-Amz-Metadata-Directive": "REPLACE"}); err != nil {
		t.Fatalf("self-copy failed: %v", err)
	}
	if stored, ok := b.loadTestMetadata(ctx, "/bucket/clear.txt"); ok && hasUserMetadata(stored) {
		t.Fatalf("metadata survived an explicit clear: %v", stored)
	}
	if stored, ok := b.loadTestMetadata(ctx, "/bucket/clear.txt"); ok && hasUserMetadata(stored) {
		t.Fatalf("clearing left user metadata behind: %v", stored)
	}
}

// A self-copy without a directive keeps the metadata the object already had.
func TestSelfCopyWithoutDirectiveKeepsMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeTestMetadata(ctx, "/bucket/keep.txt", map[string]string{"X-Amz-Meta-Pin": "deadbeef"})

	if _, err := b.replaceObjectMetadata(ctx, "/bucket/keep.txt", map[string]string{}); err != nil {
		t.Fatalf("self-copy failed: %v", err)
	}
	if stored, ok := b.loadTestMetadata(ctx, "/bucket/keep.txt"); !ok || stored["x-amz-meta-pin"] != "deadbeef" {
		t.Fatalf("plain self-copy dropped metadata: %v", stored)
	}
}

func TestSelfCopyReplaceAppliesRequestMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeTestMetadata(ctx, "/bucket/swap.txt", map[string]string{"X-Amz-Meta-Pin": "old", "X-Amz-Meta-Extra": "gone"})

	res, err := b.replaceObjectMetadata(ctx, "/bucket/swap.txt", map[string]string{
		"X-Amz-Metadata-Directive": "REPLACE",
		"X-Amz-Meta-Pin":           "new",
	})
	if err != nil {
		t.Fatalf("self-copy failed: %v", err)
	}
	if res.LastModified.IsZero() {
		t.Fatalf("self-copy CopyObjectResult.LastModified must not be zero")
	}
	stored, ok := b.loadTestMetadata(ctx, "/bucket/swap.txt")
	if !ok || stored["X-Amz-Meta-Pin"] != "new" {
		t.Fatalf("request metadata not applied: %v", stored)
	}
	if _, present := stored["x-amz-meta-extra"]; present {
		t.Fatalf("REPLACE kept metadata the request did not mention: %v", stored)
	}
}

func longTestKey(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	key := make([]byte, n)
	for i := range key {
		key[i] = alphabet[i%len(alphabet)]
	}
	return string(key)
}

// testHash stands in for the content hash of the object a test writes, so that
// the in-memory cache and the database agree on which content the metadata
// describes.
var testHash = []byte("0123456789abcdef0123456789abcdef")

// storeTestMetadata records metadata the way an upload does.
func (b *s3Backend) storeTestMetadata(ctx context.Context, name string, headers map[string]string) {
	b.storeObjectMetadata(ctx, name, headers, testHash, "")
}

// loadTestMetadata reads metadata the way a HEAD does, including the database
// fallback that makes it survive a restart.
func (b *s3Backend) loadTestMetadata(ctx context.Context, name string) (map[string]string, bool) {
	headers, _ := b.objectMetadataOf(ctx, name, testHash)
	return headers, headers != nil
}

// F. 自拷贝只改元数据后，重启必须还能看到新元数据（内存缓存不能是唯一的落点）。
func TestSelfCopyReplaceSurvivesRestart(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	const body = "0123456789abcdef"
	if _, err := b.PutObject(ctx, "mp", "restart.bin", map[string]string{
		"Content-Type":   "text/plain",
		"X-Amz-Meta-Pin": "v1",
	}, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("PutObject: %+v", err)
	}
	if _, err := b.CopyObject(ctx, "mp", "restart.bin", "mp", "restart.bin", map[string]string{
		"X-Amz-Metadata-Directive": "REPLACE",
		"X-Amz-Meta-Pin":           "v2",
	}); err != nil {
		t.Fatalf("self copy: %+v", err)
	}

	after := &s3Backend{meta: new(sync.Map)}
	obj, err := after.HeadObject(ctx, "mp", "restart.bin")
	if err != nil {
		t.Fatalf("HeadObject after restart: %+v", err)
	}
	if got := obj.Metadata["X-Amz-Meta-Pin"]; got != "v2" {
		t.Fatalf("metadata after restart = %q, want v2 (the self copy only reached the cache)", got)
	}
}

// A copy with REPLACE must describe the destination with the request, not with
// what the source carries, and must not store the directive itself as metadata.
func TestCopyReplaceDropsSourceMetadata(t *testing.T) {
	ctx := t.Context()
	b, _ := setupMultipartBackend(t)

	source := map[string]string{
		"Content-Type":    "text/plain",
		"X-Amz-Meta-Keep": "from-source",
	}
	if _, err := b.PutObject(ctx, "mp", "src.txt", source, strings.NewReader("copy me"), 7); err != nil {
		t.Fatalf("PutObject: %+v", err)
	}

	replaced := map[string]string{
		"Content-Type":             "application/json",
		"X-Amz-Metadata-Directive": "REPLACE",
	}
	if _, err := b.CopyObject(ctx, "mp", "src.txt", "mp", "dst.txt", replaced); err != nil {
		t.Fatalf("CopyObject: %+v", err)
	}

	object, err := b.HeadObject(ctx, "mp", "dst.txt")
	if err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}
	meta := object.Metadata
	if got := meta["X-Amz-Meta-Keep"]; got != "" {
		t.Fatalf("REPLACE kept the source metadata: X-Amz-Meta-Keep = %q", got)
	}
	if got := metadataDirective(meta); got != "" {
		t.Fatalf("the copy directive was stored as metadata: %q", got)
	}
	if got := meta["Content-Type"]; got != "application/json" {
		t.Fatalf("Content-Type = %q, want the value the request asked for", got)
	}
}
