package s3

import (
	"context"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// metadataRow loads the s3_object_metadata row of an object path.
func metadataRow(t *testing.T, name string) *model.S3ObjectMetadata {
	t.Helper()
	var row model.S3ObjectMetadata
	err := db.GetDb().Where("key_hash = ?", model.HashS3ObjectMetadataKey(name)).Take(&row).Error
	if err != nil {
		if err.Error() == "record not found" {
			return nil
		}
		t.Fatalf("load row for %q: %v", name, err)
	}
	return &row
}

// recordRead buffers one read of an object and writes it, as the gateway does.
func recordRead(t *testing.T, b *s3Backend, name string, at time.Time) {
	t.Helper()
	b.access.mu.Lock()
	if b.access.pending == nil {
		b.access.pending = map[string]time.Time{}
	}
	b.access.pending[name] = at
	b.access.mu.Unlock()
	b.flushAccess()
}

// User metadata and the access record share one row, but they belong to
// different events. A plain re-upload that carries no x-amz-meta-* used to delete
// the row - and with it the time the object was last read - so a hot object fell
// out of the access listing and the write throttle then kept it out for an hour.
func TestReuploadWithoutUserMetadataKeepsAccessRecord(t *testing.T) {
	resetMetadataTable(t)
	b := newBackend().(*s3Backend)
	name := "/metadata-access/hot.bin"
	read := time.Now().UTC().Truncate(time.Second)

	recordRead(t, b, name, read)
	before := metadataRow(t, name)
	if before == nil || before.LastAccess == nil {
		t.Fatal("the read was not recorded")
	}

	// `aws s3 cp` sends a Content-Type and no user metadata.
	b.storeObjectMetadata(context.Background(), name, map[string]string{"Content-Type": "video/mp4"}, nil, "")

	after := metadataRow(t, name)
	if after == nil {
		t.Fatal("the re-upload deleted the access record")
	}
	if after.LastAccess == nil || !after.LastAccess.Equal(read) {
		t.Fatalf("last_access = %v, want %v", after.LastAccess, read)
	}
	if headers, ok := b.loadStoredMetadata(context.Background(), name); ok && len(headers) > 0 && headers["Content-Type"] != "video/mp4" {
		t.Fatalf("headers = %v, want the Content-Type of the upload", headers)
	}

	// The headers of an object that carries user metadata are still replaced.
	b.storeObjectMetadata(context.Background(), name, map[string]string{"x-amz-meta-owner": "ops"}, nil, "")
	withMeta := metadataRow(t, name)
	if withMeta == nil || withMeta.LastAccess == nil || !withMeta.LastAccess.Equal(read) {
		t.Fatal("storing user metadata dropped the access record")
	}
	b.storeObjectMetadata(context.Background(), name, map[string]string{"Content-Type": "video/mp4"}, nil, "")
	cleared := metadataRow(t, name)
	if cleared == nil || cleared.LastAccess == nil {
		t.Fatal("clearing user metadata dropped the access record")
	}
	// NULL or an empty object: what matters is that no user metadata survived.
	if stored, ok := b.loadStoredMetadata(context.Background(), name); ok {
		if _, still := stored["x-amz-meta-owner"]; still {
			t.Fatalf("user metadata survived a write that carried none: %v", stored)
		}
	}
}

// Two flushes can commit out of order; the stored time must only move forward, or
// a hot object is recorded as read an hour ago and tiering moves it to cold
// storage.
func TestS3AccessTimeOnlyMovesForward(t *testing.T) {
	resetMetadataTable(t)
	b := newBackend().(*s3Backend)
	name := "/metadata-access/forward.bin"
	newest := time.Now().UTC().Truncate(time.Second)
	older := newest.Add(-time.Hour)

	recordRead(t, b, name, newest)
	// The second batch has to reach the database: without dropping the throttle
	// entry the flush would skip it and never exercise the conflict update.
	b.access.mu.Lock()
	delete(b.access.written, name)
	b.access.mu.Unlock()
	recordRead(t, b, name, older)

	stored := metadataRow(t, name)
	if stored == nil || stored.LastAccess == nil {
		t.Fatal("the read was not recorded")
	}
	if !stored.LastAccess.Equal(newest) {
		t.Fatalf("last_access = %v, want the newest %v", stored.LastAccess, newest)
	}
}

// A restart used to drop whatever the trackers had buffered, which is exactly the
// reads a hot/cold split depends on. The shutdown flush is what the test drives.
func TestFlushAccessWritesBufferedReads(t *testing.T) {
	resetMetadataTable(t)
	b := newBackend().(*s3Backend)
	name := "/metadata-access/shutdown.bin"
	read := time.Now().UTC().Truncate(time.Second)

	b.access.mu.Lock()
	if b.access.pending == nil {
		b.access.pending = map[string]time.Time{}
	}
	b.access.pending[name] = read
	b.access.mu.Unlock()

	FlushAccess()

	stored := metadataRow(t, name)
	if stored == nil || stored.LastAccess == nil {
		t.Fatal("FlushAccess did not write the buffered read")
	}
	if !stored.LastAccess.Equal(read) {
		t.Fatalf("last_access = %v, want %v", stored.LastAccess, read)
	}
}
