package s3

import (
	"context"
	"os"
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
	os.Exit(m.Run())
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
	before.storeObjectMetadata(ctx, "/bucket/meta.txt", headers)

	after := &s3Backend{meta: new(sync.Map)}
	stored, ok := after.loadObjectMetadata(ctx, "/bucket/meta.txt")
	if !ok || stored["X-Amz-Meta-Pin"] != "deadbeef" {
		t.Fatalf("metadata lost across a restart: %v", stored)
	}
}

// The common case must not pay for a row.
func TestPlainUploadCreatesNoRow(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeObjectMetadata(ctx, "/bucket/plain.txt", map[string]string{"Content-Type": "text/plain"})

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
	b.storeObjectMetadata(ctx, "/bucket/m.txt", map[string]string{"X-Amz-Meta-Pin": "old"})
	b.storeObjectMetadata(ctx, "/bucket/m.txt", map[string]string{"Content-Type": "text/plain"})

	if _, ok := b.loadStoredMetadata(ctx, "/bucket/m.txt"); ok {
		t.Fatal("stale metadata survived an overwrite")
	}
	if rows := countMetadataRows(t); rows != 0 {
		t.Fatalf("overwrite left %d row(s) behind", rows)
	}
}

func TestDeleteRemovesPersistedMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeObjectMetadata(ctx, "/bucket/gone.txt", map[string]string{"X-Amz-Meta-Pin": "deadbeef"})
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
	b.storeObjectMetadata(ctx, "/bucket/src.txt", map[string]string{"X-Amz-Meta-Pin": "carried"})
	// The destination upload itself carries no user metadata, which is what a
	// COPY directive does before the recorded copy is applied.
	b.storeObjectMetadata(ctx, "/bucket/dst.txt", map[string]string{"Content-Type": "text/plain"})
	b.copyStoredMetadata(ctx, "/bucket/src.txt", "/bucket/dst.txt")

	stored, ok := b.loadStoredMetadata(ctx, "/bucket/dst.txt")
	if !ok || stored["X-Amz-Meta-Pin"] != "carried" {
		t.Fatalf("copy did not carry metadata: %v", stored)
	}
	if source, ok := b.loadStoredMetadata(ctx, "/bucket/src.txt"); !ok || source["X-Amz-Meta-Pin"] != "carried" {
		t.Fatalf("copy damaged the source metadata: %v", source)
	}
}

// The unique index uses a hash, so the path itself may be arbitrarily long.
func TestLongPathMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()
	name := "/bucket/" + longTestKey(300)

	b := &s3Backend{meta: new(sync.Map)}
	b.storeObjectMetadata(ctx, name, map[string]string{"X-Amz-Meta-Pin": "deadbeef"})

	if stored, ok := b.loadStoredMetadata(ctx, name); !ok || stored["X-Amz-Meta-Pin"] != "deadbeef" {
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
	b.storeObjectMetadata(ctx, "/bucket/clear.txt", map[string]string{"X-Amz-Meta-Pin": "deadbeef"})

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
	b.storeObjectMetadata(ctx, "/bucket/keep.txt", map[string]string{"X-Amz-Meta-Pin": "deadbeef"})

	if _, err := b.replaceObjectMetadata(ctx, "/bucket/keep.txt", map[string]string{}); err != nil {
		t.Fatalf("self-copy failed: %v", err)
	}
	if stored, ok := b.loadObjectMetadata(ctx, "/bucket/keep.txt"); !ok || stored["X-Amz-Meta-Pin"] != "deadbeef" {
		t.Fatalf("plain self-copy dropped metadata: %v", stored)
	}
}

func TestSelfCopyReplaceAppliesRequestMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeObjectMetadata(ctx, "/bucket/swap.txt", map[string]string{"X-Amz-Meta-Pin": "old", "X-Amz-Meta-Extra": "gone"})

	if _, err := b.replaceObjectMetadata(ctx, "/bucket/swap.txt", map[string]string{
		"X-Amz-Metadata-Directive": "REPLACE",
		"X-Amz-Meta-Pin":           "new",
	}); err != nil {
		t.Fatalf("self-copy failed: %v", err)
	}
	stored, ok := b.loadObjectMetadata(ctx, "/bucket/swap.txt")
	if !ok || stored["X-Amz-Meta-Pin"] != "new" {
		t.Fatalf("request metadata not applied: %v", stored)
	}
	if _, present := stored["X-Amz-Meta-Extra"]; present {
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
