package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// A. derivedEtag 是否真的随修改时间变化？
func TestDerivedEtagDependsOnModificationTime(t *testing.T) {
	t1 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix()
	t2 := t1 + 1 // 同一对象、同样大小，只是修改时间晚了 1 秒
	h1 := derivedEtag("bucket/x.bin", 4096, t1)
	h2 := derivedEtag("bucket/x.bin", 4096, t2)
	if bytes.Equal(h1, h2) {
		t.Fatalf("derivedEtag ignores the modification time: %x == %x", h1, h2)
	}
}

// B. persistedIdentityHash 写进库里的 (size, modtime) 能否在下次读时命中？
func TestRecordedIdentityStoresFullPrecision(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeVersion)
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	const name = "/audit/oob.bin"
	const size = 4096
	modNanos := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixNano()

	b.persistedIdentityHash(ctx, name, size, modNanos)

	row, ok := loadStoredRow(ctx, name)
	if !ok {
		t.Fatal("no row was recorded")
	}
	// The modification time is kept at full precision: truncating it let a
	// rewrite inside the same second reuse the ETag of the content it replaced.
	if want := modNanos; row.ModTime != want {
		t.Fatalf("recorded mod_time = %d, want %d (nanoseconds)", row.ModTime, want)
	}
	if row.Size != size {
		t.Fatalf("recorded size = %d, want %d", row.Size, size)
	}
}

// C. 第二次读必须走「已记录」路径（而不是每次都重写库）。
func TestRecordedIdentityIsReusedOnNextRead(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeVersion)
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	const name = "/audit/hit.bin"
	const size = 8192
	modNanos := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC).UnixNano()

	first := b.persistedIdentityHash(ctx, name, size, modNanos)
	// 把库里那行的 etag 换成哨兵值：只有命中「已记录」路径才会读回它。
	if err := db.GetDb().Model(&model.S3ObjectMetadata{}).Where("path = ?", name).
		Update("etag", "deadbeefdeadbeefdeadbeefdeadbeef").Error; err != nil {
		t.Fatalf("update row: %v", err)
	}
	second := b.persistedIdentityHash(ctx, name, size, modNanos)
	if bytes.Equal(first, second) {
		t.Fatalf("second read re-derived the ETag instead of using the recorded row: %x", second)
	}
}

// D. 拷贝出来的对象，其 HEAD 必须与 CopyObject 返回的 ETag 一致。
func TestCopyReportsTheEtagItRecords(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeVersion)
	ctx := context.Background()
	b, root := setupMultipartBackend(t)

	// 直接写盘：模拟绕过 S3 网关、在网盘上已有的对象（没有记录，ETag 靠派生）。
	if err := os.WriteFile(filepath.Join(root, "oob.bin"), []byte("0123456789abcdef"), 0o644); err != nil {
		t.Fatalf("write object: %v", err)
	}
	src, err := b.HeadObject(ctx, "mp", "oob.bin")
	if err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}
	srcEtag := etagOf(t, src)

	res, err := b.CopyObject(ctx, "mp", "oob.bin", "mp", "copy.bin", map[string]string{})
	if err != nil {
		t.Fatalf("CopyObject: %+v", err)
	}
	got := strings.Trim(res.ETag, `"`)

	dst, err := b.HeadObject(ctx, "mp", "copy.bin")
	if err != nil {
		t.Fatalf("HeadObject(dst): %+v", err)
	}
	if headEtag := etagOf(t, dst); headEtag != got {
		t.Fatalf("copy returned ETag %s but HEAD of the copy says %s (source %s)", got, headEtag, srcEtag)
	}
}

// E. 只改元数据的自拷贝，也要把对象身份一起返回。
func TestSelfCopyReportsTheObjectIdentity(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeVersion)
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	const body = "0123456789abcdef"
	_, err := b.PutObject(ctx, "mp", "self.bin", map[string]string{
		"Content-Type":   "text/plain",
		"X-Amz-Meta-Pin": "v1",
	}, strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("PutObject: %+v", err)
	}

	res, err := b.CopyObject(ctx, "mp", "self.bin", "mp", "self.bin", map[string]string{
		"X-Amz-Metadata-Directive": "REPLACE",
		"X-Amz-Meta-Pin":           "v2",
	})
	if err != nil {
		t.Fatalf("self copy: %+v", err)
	}
	if res.ETag == "" {
		t.Fatal("self copy returned an empty ETag")
	}
	if got, want := strings.Trim(res.ETag, `"`), md5Hex(body); got != want {
		t.Fatalf("self copy ETag = %s, want the object ETag %s", got, want)
	}

	obj, err := b.HeadObject(ctx, "mp", "self.bin")
	if err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}
	if got := etagOf(t, obj); got != strings.Trim(res.ETag, `"`) {
		t.Fatalf("HEAD after self copy = %s, want %s", got, strings.Trim(res.ETag, `"`))
	}
	if got := obj.Metadata["X-Amz-Meta-Pin"]; got != "v2" {
		t.Fatalf("metadata after self copy = %q, want v2", got)
	}
}

// A rewrite that keeps the size and lands in the same second must still produce
// a new identity, or a conditional write carrying the pre-rewrite ETag passes its
// If-Match and overwrites the other writer's data.
func TestPersistedIdentityDetectsSameSecondRewrite(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeVersion)
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	const name = "/audit/same-second.bin"
	const size = 10
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	before := b.persistedIdentityHash(ctx, name, size, base.UnixNano())
	// Same size, same second, one nanosecond later: content replaced in place.
	after := b.persistedIdentityHash(ctx, name, size, base.Add(time.Nanosecond).UnixNano())

	if bytes.Equal(before, after) {
		t.Fatal("a rewrite with the same size and second kept the old identity: If-Match would accept a stale ETag")
	}
}

// The ETag a HEAD reports has to be available to the If-Range check too, even
// when the metadata cache is cold: a database hit used to return an empty ETag,
// so a strong If-Range never matched and the range was dropped.
func TestObjectMetadataOfReturnsEtagOnDatabaseHit(t *testing.T) {
	resetMetadataTable(t)
	useEtagMode(t, etagModeVersion)
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	const name = "/audit/cold-cache.bin"
	hash := md5.Sum([]byte("content of the object"))
	headers := map[string]string{"x-amz-meta-keep": "yes"}
	b.storeObjectMetadata(ctx, name, headers, hash[:], `"`+hex.EncodeToString(hash[:])+`"`)

	// A backend whose cache knows nothing about the object answers from the
	// database, which is the path a restart, an eviction or another instance
	// leave behind.
	cold := newBackend().(*s3Backend)
	cold.meta.Delete(name)
	stored, etag := cold.objectMetadataOf(ctx, name, hash[:])
	if stored == nil {
		t.Fatal("stored metadata was not found")
	}
	want := `"` + hex.EncodeToString(hash[:]) + `"`
	if etag != want {
		t.Fatalf("etag from the database = %q, want %q", etag, want)
	}
}
