package s3

import (
	"context"
	"path"
	"strings"
	"sync"
	"testing"
)

// An object deleted and then uploaded again under the same key is a new object,
// so reads of it are new reads. The tombstone a delete leaves behind used to
// swallow them for ever: after one re-upload the access list showed the object
// as never read, and the hot/cold split archived an object that was in fact
// being read.
func TestReadOfReuploadedObjectIsRecorded(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	bucket, err := getBucketByName("mp")
	if err != nil {
		t.Fatalf("bucket: %v", err)
	}
	fp := path.Join(bucket.Path, "logs/app.log")

	write := func() {
		t.Helper()
		if _, err := b.PutObject(ctx, "mp", "logs/app.log", map[string]string{}, strings.NewReader("hello"), 5); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	read := func() {
		t.Helper()
		obj, err := b.GetObject(ctx, "mp", "logs/app.log", nil)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		_ = obj.Contents.Close()
	}

	write()
	read()
	b.flushAccess()
	if row := accessRow(t, fp); row == nil || row.LastAccess == nil {
		t.Fatal("the first read was not recorded")
	}

	// The delete drops the row and leaves the path tombstoned so that a batch
	// collected before it cannot write the row back.
	if _, err := b.DeleteObject(ctx, "mp", "logs/app.log"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if row := accessRow(t, fp); row != nil {
		t.Fatal("the row of the deleted object is still there")
	}

	// Upload it again and read it: this read is newer than the delete.
	write()
	read()
	b.flushAccess()

	if row := accessRow(t, fp); row == nil || row.LastAccess == nil {
		t.Fatal("the read of the re-uploaded object was not recorded")
	}
}

// A delete that lands while an access batch is in flight must remove only the
// access row of the deleted object. Uploading the same key again inside that
// window - the delete+PUT pair a sync tool performs - used to lose the new
// object's user metadata and its recorded ETag: the cleanup deleted the row by
// path, headers and all, and the next process answered a GET/HEAD without them.
func TestReuploadDuringFlushKeepsItsMetadata(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()
	b := &s3Backend{meta: new(sync.Map)}
	const p = "/audit/round14/re-upload.bin"

	b.storeObjectMetadata(ctx, p, map[string]string{"X-Amz-Meta-Owner": "first"})
	if row := accessRow(t, p); row == nil {
		t.Fatal("the upload did not store a row")
	}

	// One buffered read, and the batch is about to be written.
	b.recordAccess(p)
	b.flushTestHook = func() {
		b.flushTestHook = nil
		// The object is deleted and uploaded again, all while the collected
		// batch is still on its way to the database.
		b.deleteStoredMetadata(ctx, p)
		b.access.forget(p)
		b.storeObjectMetadata(ctx, p, map[string]string{"X-Amz-Meta-Owner": "second"})
	}
	b.flushAccess()

	// Read back from the database, which is what a restarted process sees.
	headers, ok := b.loadStoredMetadata(ctx, p)
	if !ok {
		t.Fatal("the re-uploaded object lost its metadata row")
	}
	if got := headers["x-amz-meta-owner"]; got != "second" {
		t.Fatalf("metadata of the re-uploaded object = %q, want %q", got, "second")
	}
}
