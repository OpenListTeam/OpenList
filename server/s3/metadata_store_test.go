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
	headers := map[string]string{"Content-Type": "text/plain", "x-amz-meta-pin": "deadbeef"}

	before := &s3Backend{meta: new(sync.Map)}
	before.storeObjectMetadata(ctx, "/bucket/meta.txt", headers)

	after := &s3Backend{meta: new(sync.Map)}
	stored, ok := after.loadObjectMetadata(ctx, "/bucket/meta.txt")
	if !ok || stored["x-amz-meta-pin"] != "deadbeef" {
		t.Fatalf("metadata lost across a restart: %v", stored)
	}
}

// An upload that carries no user metadata still keeps its ETag, which is the one
// thing that cannot be recovered from the stored bytes without reading them.
func TestPlainUploadKeepsOnlyItsETagRow(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeObjectMetadata(ctx, "/bucket/plain.txt", map[string]string{
		"Content-Type":  "text/plain",
		etagMetadataKey: "9dd4e461268c8034f5c8564e155c67a6",
	})

	stored, ok := b.loadStoredMetadata(ctx, "/bucket/plain.txt")
	if !ok {
		t.Fatal("the ETag of a plain upload was not kept")
	}
	if stored[etagMetadataKey] != "9dd4e461268c8034f5c8564e155c67a6" {
		t.Fatalf("ETag row is wrong: %v", stored)
	}
	for key := range stored {
		if strings.HasPrefix(strings.ToLower(key), userMetadataPrefix) {
			t.Fatalf("a plain upload stored user metadata: %v", stored)
		}
	}
}

// S3 replaces metadata on every upload, so an overwrite that carries none must
// not let the previous metadata describe the new content.
func TestOverwriteWithoutUserMetadataDropsStaleRow(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeObjectMetadata(ctx, "/bucket/m.txt", map[string]string{"x-amz-meta-pin": "old"})
	b.storeObjectMetadata(ctx, "/bucket/m.txt", map[string]string{
		"Content-Type":  "text/plain",
		etagMetadataKey: "d41d8cd98f00b204e9800998ecf8427e",
	})

	stored, ok := b.loadStoredMetadata(ctx, "/bucket/m.txt")
	if !ok {
		t.Fatal("the row of the new content disappeared")
	}
	for key := range stored {
		if strings.HasPrefix(strings.ToLower(key), userMetadataPrefix) {
			t.Fatalf("stale user metadata survived an overwrite: %v", stored)
		}
	}
}

func TestDeleteRemovesPersistedMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeObjectMetadata(ctx, "/bucket/gone.txt", map[string]string{"x-amz-meta-pin": "deadbeef"})
	b.meta.Delete("/bucket/gone.txt")
	b.deleteStoredMetadata(ctx, "/bucket/gone.txt")

	if _, ok := b.loadObjectMetadata(ctx, "/bucket/gone.txt"); ok {
		t.Fatal("metadata survived deletion")
	}
}

func TestCopyCarriesMetadataToDestination(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeObjectMetadata(ctx, "/bucket/src.txt", map[string]string{"x-amz-meta-pin": "carried"})
	// The destination upload itself carries no user metadata, which is what a
	// COPY directive does before the recorded copy is applied.
	b.storeObjectMetadata(ctx, "/bucket/dst.txt", map[string]string{"Content-Type": "text/plain"})
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
	b.storeObjectMetadata(ctx, name, map[string]string{"x-amz-meta-pin": "deadbeef"})

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
	b.storeObjectMetadata(ctx, "/bucket/clear.txt", map[string]string{"x-amz-meta-pin": "deadbeef"})

	if _, err := b.replaceObjectMetadata(ctx, "/bucket/clear.txt", map[string]string{"X-Amz-Metadata-Directive": "REPLACE"}); err != nil {
		t.Fatalf("self-copy failed: %v", err)
	}
	if stored, ok := b.loadObjectMetadata(ctx, "/bucket/clear.txt"); ok && hasUserMetadata(stored) {
		t.Fatalf("metadata survived an explicit clear: %v", stored)
	}
	if rows := countMetadataRows(t); rows != 0 {
		t.Fatalf("clearing left %d row(s) behind", rows)
	}
}

// A self-copy without a directive keeps the metadata the object already had.
func TestSelfCopyWithoutDirectiveKeepsMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeObjectMetadata(ctx, "/bucket/keep.txt", map[string]string{"x-amz-meta-pin": "deadbeef"})

	if _, err := b.replaceObjectMetadata(ctx, "/bucket/keep.txt", map[string]string{}); err != nil {
		t.Fatalf("self-copy failed: %v", err)
	}
	if stored, ok := b.loadObjectMetadata(ctx, "/bucket/keep.txt"); !ok || stored["x-amz-meta-pin"] != "deadbeef" {
		t.Fatalf("plain self-copy dropped metadata: %v", stored)
	}
}

func TestSelfCopyReplaceAppliesRequestMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeObjectMetadata(ctx, "/bucket/swap.txt", map[string]string{"x-amz-meta-pin": "old", "x-amz-meta-extra": "gone"})

	res, err := b.replaceObjectMetadata(ctx, "/bucket/swap.txt", map[string]string{
		"X-Amz-Metadata-Directive": "REPLACE",
		"x-amz-meta-pin":           "new",
	})
	if err != nil {
		t.Fatalf("self-copy failed: %v", err)
	}
	if res.LastModified.IsZero() {
		t.Fatalf("self-copy CopyObjectResult.LastModified must not be zero")
	}
	stored, ok := b.loadObjectMetadata(ctx, "/bucket/swap.txt")
	if !ok || stored["x-amz-meta-pin"] != "new" {
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

// A metadata-only self copy must reach the database, not just the cache, or the
// previous metadata comes back after a restart.
func TestSelfCopyReplaceSurvivesRestart(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	const body = "0123456789abcdef"
	if _, err := b.PutObject(ctx, "mp", "restart.bin", map[string]string{
		"Content-Type":   "text/plain",
		"x-amz-meta-pin": "v1",
	}, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("PutObject: %+v", err)
	}
	if _, err := b.CopyObject(ctx, "mp", "restart.bin", "mp", "restart.bin", map[string]string{
		"X-Amz-Metadata-Directive": "REPLACE",
		"x-amz-meta-pin":           "v2",
	}); err != nil {
		t.Fatalf("self copy: %+v", err)
	}

	after := &s3Backend{meta: new(sync.Map)}
	obj, err := after.HeadObject(ctx, "mp", "restart.bin")
	if err != nil {
		t.Fatalf("HeadObject after restart: %+v", err)
	}
	if got := obj.Metadata["x-amz-meta-pin"]; got != "v2" {
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
		"x-amz-meta-keep": "from-source",
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
	if got := meta["x-amz-meta-keep"]; got != "" {
		t.Fatalf("REPLACE kept the source metadata: x-amz-meta-keep = %q", got)
	}
	if got := metadataDirective(meta); got != "" {
		t.Fatalf("the copy directive was stored as metadata: %q", got)
	}
	if got := meta["Content-Type"]; got != "application/json" {
		t.Fatalf("Content-Type = %q, want the value the request asked for", got)
	}
}

// User metadata keys come back in the shape AWS documents — "x-amz-meta-" plus a
// lowercase suffix. Passing the Go-canonicalised "X-Amz-Meta-Team" through made
// clients miss the key they had sent (boto3: obj["Metadata"]["team"] empty).
func TestUserMetadataKeysComeBackLowercase(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	if _, err := b.PutObject(ctx, "mp", "meta-case.txt",
		map[string]string{"X-Amz-Meta-Team": "cold-tier", "Content-Type": "text/plain"},
		strings.NewReader("x"), 1); err != nil {
		t.Fatalf("PutObject: %+v", err)
	}

	obj, err := b.GetObject(ctx, "mp", "meta-case.txt", nil)
	if err != nil {
		t.Fatalf("GetObject: %+v", err)
	}
	if got := obj.Metadata["x-amz-meta-team"]; got != "cold-tier" {
		t.Errorf("metadata key is not the AWS shape: %v", obj.Metadata)
	}
	if _, present := obj.Metadata["X-Amz-Meta-Team"]; present {
		t.Errorf("canonicalised key leaked into the response: %v", obj.Metadata)
	}
}

// Rows written before the normalisation keep the canonicalised spelling; they
// must still be readable, and must be reported in the AWS shape.
func TestLegacyMetadataRowsAreNormalisedOnRead(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	b.storeObjectMetadata(ctx, "/bucket/legacy.txt", map[string]string{
		"X-Amz-Meta-Team": "cold-tier",
		"Content-Type":    "text/plain",
	})
	stored, ok := b.loadObjectMetadata(ctx, "/bucket/legacy.txt")
	if !ok {
		t.Fatal("metadata row disappeared")
	}
	if got := stored["x-amz-meta-team"]; got != "cold-tier" {
		t.Errorf("legacy key not normalised on read: %v", stored)
	}
}
