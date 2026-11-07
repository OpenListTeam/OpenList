package s3

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/OpenListTeam/gofakes3"
)

// TestMultipartUploadOwnership pins the rule that an upload ID is only usable
// by the bucket and key it was created for. gofakes3 reads the bucket and the
// key from the URL of each step, so before this was enforced any key that could
// write bucket A could append a part to a foreign upload and complete it into
// an object of its own choosing.
func TestMultipartUploadOwnership(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "owned.txt", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}

	stateVal, ok := b.uploads.Load(uploadID)
	if !ok {
		t.Fatal("upload missing from the backend right after creation")
	}
	state := stateVal.(*multipartState)

	// Another key must not be able to drop a part into it.
	if _, err := b.UploadPart(ctx, "mp", "other.txt", uploadID, 1, 4, strings.NewReader("data")); s3ErrorCode(err) != gofakes3.ErrNoSuchUpload {
		t.Fatalf("foreign UploadPart error = %v, want NoSuchUpload", err)
	}
	state.mu.Lock()
	partsAfterForeign := len(state.parts)
	state.mu.Unlock()
	if partsAfterForeign != 0 {
		t.Fatalf("foreign UploadPart stored %d part(s), want 0", partsAfterForeign)
	}

	// The owner still can.
	etag, err := b.UploadPart(ctx, "mp", "owned.txt", uploadID, 1, 4, strings.NewReader("data"))
	if err != nil {
		t.Fatalf("owner UploadPart: %v", err)
	}

	// And the upload must not be completable into a different key.
	_, _, err = b.CompleteMultipartUpload(ctx, "mp", "other.txt", uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: etag}},
	})
	if s3ErrorCode(err) != gofakes3.ErrNoSuchUpload {
		t.Fatalf("foreign CompleteMultipartUpload error = %v, want NoSuchUpload", err)
	}
	if _, ok := b.uploads.Load(uploadID); !ok {
		t.Fatal("a refused complete must leave the upload in place for its owner")
	}

	// Abort is idempotent, but a foreign caller must not cancel someone else's
	// upload either.
	if err := b.AbortMultipartUpload(ctx, "mp", "other.txt", uploadID); err != nil {
		t.Fatalf("AbortMultipartUpload by a non-owner returned %v, want nil", err)
	}
	if _, ok := b.uploads.Load(uploadID); !ok {
		t.Fatal("a foreign abort cancelled the upload")
	}

	// The owner can still finish the upload.
	_, etag, err = b.CompleteMultipartUpload(ctx, "mp", "owned.txt", uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: etag}},
	})
	if err != nil {
		t.Fatalf("owner CompleteMultipartUpload: %v", err)
	}
	if etag == "" {
		t.Fatal("owner CompleteMultipartUpload returned an empty etag")
	}
}

// TestMultipartUploadOwnershipWrongBucket covers the bucket half of the check,
// which cannot be reached through a normal route (bucket auth would stop it
// first) but must still hold if two buckets ever share a key.
func TestMultipartUploadOwnershipWrongBucket(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "owned.txt", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	if _, err := b.UploadPart(ctx, "other-bucket", "owned.txt", uploadID, 1, 4, bytes.NewReader([]byte("data"))); s3ErrorCode(err) != gofakes3.ErrNoSuchUpload {
		t.Fatalf("wrong-bucket UploadPart error = %v, want NoSuchUpload", err)
	}
}
