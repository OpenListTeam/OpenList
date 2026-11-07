package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestAuditRound38_ObjectMetadataOfEmptyHeaders verifies that objects without
// custom user metadata (headers == nil, typical in performance mode WAL bypass)
// correctly return their cached or derived ETag rather than returning empty strings.
func TestAuditRound38_ObjectMetadataOfEmptyHeaders(t *testing.T) {
	b := &s3Backend{meta: new(sync.Map)}
	ctx := context.Background()
	name := "bench-bucket/block-001.bin"
	rawHash := md5.Sum([]byte("block content"))
	expectedETag := fmt.Sprintf("%q", hex.EncodeToString(rawHash[:]))

	// 1. In-memory cache hit with headers == nil (standard PUT without x-amz-meta-*)
	b.cacheObjectMetadata(name, objectMetadata{
		headers: nil,
		hash:    rawHash[:],
		etag:    expectedETag,
	})

	headers, etag := b.objectMetadataOf(ctx, name, rawHash[:])
	if etag != expectedETag {
		t.Fatalf("expected cached etag %s, got %q (headers=%v)", expectedETag, etag, headers)
	}

	// 2. Cold cache (evicted or process restarted, and DB row bypassed)
	b.meta.Delete(name)
	headers, etag = b.objectMetadataOf(ctx, name, rawHash[:])
	if etag != expectedETag {
		t.Fatalf("expected cold derived etag %s, got %q (headers=%v)", expectedETag, etag, headers)
	}

	// 3. Verify it repopulated the memory cache for subsequent calls
	if value, ok := b.meta.Load(name); !ok {
		t.Fatal("expected objectMetadataOf to repopulate in-memory cache on cold miss")
	} else {
		cached := value.(objectMetadata)
		if cached.etag != expectedETag {
			t.Fatalf("repopulated cache has etag %q, want %s", cached.etag, expectedETag)
		}
	}
}

// TestAuditRound38_CopySourcePreconditionsCold verifies that copySourcePreconditionFailed
// handles empty/cold DB rows cleanly without spurious 412 or 304 errors.
func TestAuditRound38_CopySourcePreconditionsCold(t *testing.T) {
	correctETag := `"5eb63bbbe01eeed093cb22bb8f5acdc3"`
	wrongETag := `"ffffffffffffffffffffffffffffffff"`
	now := time.Now().UTC()

	// If-Match
	if copySourcePreconditionFailed(correctETag, "", "", "", correctETag, now) {
		t.Fatalf("X-Amz-Copy-Source-If-Match should pass when ETag matches %s", correctETag)
	}
	if !copySourcePreconditionFailed(wrongETag, "", "", "", correctETag, now) {
		t.Fatalf("X-Amz-Copy-Source-If-Match should fail when ETag does not match %s", wrongETag)
	}

	// If-None-Match
	if !copySourcePreconditionFailed("", correctETag, "", "", correctETag, now) {
		t.Fatalf("X-Amz-Copy-Source-If-None-Match should fail when ETag matches %s", correctETag)
	}
	if copySourcePreconditionFailed("", wrongETag, "", "", correctETag, now) {
		t.Fatalf("X-Amz-Copy-Source-If-None-Match should pass when ETag does not match %s", wrongETag)
	}
}

// TestAuditRound38_IfRangeColdEtagValidation verifies that ifRangeMatchesValidator
// correctly matches both unquoted and quoted strong ETags.
func TestAuditRound38_IfRangeColdEtagValidation(t *testing.T) {
	tag := `"5eb63bbbe01eeed093cb22bb8f5acdc3"`
	now := time.Now().UTC()

	if !ifRangeMatchesValidator(tag, tag, now) {
		t.Fatalf("ifRangeMatchesValidator should match exact strong tag %s", tag)
	}

	if ifRangeMatchesValidator(`"different"`, tag, now) {
		t.Fatalf("ifRangeMatchesValidator should not match different tag")
	}

	if ifRangeMatchesValidator(tag, "", now) {
		t.Fatalf("ifRangeMatchesValidator must reject empty current ETag")
	}
}
