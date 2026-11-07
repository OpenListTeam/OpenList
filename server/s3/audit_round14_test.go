package s3

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/OpenListTeam/gofakes3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A key whose rows are (A read-only), (B read-write), (C read-write): the third
// bucket must still be writable.
func TestDuplicateKeyRowsKeepEveryWritableBucket(t *testing.T) {
	raw := `[
	 {"access_key":"key1","secret_access_key":"s","buckets":["a"],"readonly":true},
	 {"access_key":"key1","secret_access_key":"s","buckets":["b"],"readonly":false},
	 {"access_key":"key1","secret_access_key":"s","buckets":["c"],"readonly":false}
	]`
	rules, misconfigured := parseS3BucketAuthRules(raw, "", "")
	if misconfigured {
		t.Fatalf("unexpectedly misconfigured")
	}
	if len(rules) != 1 {
		t.Fatalf("expected one merged rule, got %d", len(rules))
	}
	r := rules[0]
	for _, b := range []string{"b", "c"} {
		if !r.MatchesBucket(b) {
			t.Fatalf("%s should match", b)
		}
		if !r.CanWriteBucket(b) {
			t.Fatalf("%s must stay writable, writableBuckets=%v readonly=%v", b, r.writableBuckets, r.ReadOnly)
		}
	}
	if r.CanWriteBucket("a") {
		t.Fatalf("a was marked read-only")
	}
}

// Copying the key a listing invented for an empty folder must not panic: the
// source is not a stored object, so fs.Get fails while GetObject still answers
// it, and the mtime/last-modified it reports needs a node that exists.
func TestCopyEmptyFolderSentinelDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	backend, _ := setupMultipartBackend(t)
	putTestObject(t, backend, ctx, "d/x.txt")
	if _, err := backend.DeleteObject(ctx, "mp", "d/x.txt"); err != nil {
		t.Fatalf("delete object: %v", err)
	}
	src := "d/" + emptyObjectName
	if _, err := backend.GetObject(ctx, "mp", src, nil); err != nil {
		t.Fatalf("sentinel should be readable: %v", err)
	}
	if _, err := backend.CopyObject(ctx, "mp", src, "mp", "copy.txt", map[string]string{}); err != nil {
		t.Fatalf("copy of sentinel: %v", err)
	}
}

// The admin save hook exists to refuse credentials the gateway would silently
// drop, so a key gofakes3 cannot even parse must not be accepted there either.
func TestSettingHookRejectsShortAccessKey(t *testing.T) {
	err := checkS3BucketAuthSetting(`[{"access_key":"ab","secret_access_key":"s","buckets":["a"]}]`)
	if err == nil {
		t.Fatalf("a 2-character access key must be refused at save time")
	}
	if err := checkS3BucketAuthSetting(`[{"access_key":"abc","secret_access_key":"s","buckets":["a"]}]`); err != nil {
		t.Fatalf("a 3-character access key is accepted: %v", err)
	}
}

// Changing an object's Content-Type without rewriting its content is done with a
// self-copy carrying x-amz-metadata-directive: REPLACE and the new header - the
// workflow AWS documents. The copy-to-another-key path honours the object headers
// of such a request, so the in-place path has to as well; keeping only the user
// metadata dropped the header the request asked to set and erased the one the
// object already had.
func TestSelfCopyReplaceAppliesObjectHeaders(t *testing.T) {
	resetMetadataTable(t)
	ctx := context.Background()

	b := &s3Backend{meta: new(sync.Map)}
	b.storeObjectMetadata(ctx, "/bucket/type.txt", map[string]string{
		"Content-Type":   "text/plain",
		"x-amz-meta-pin": "v1",
	})

	if _, err := b.replaceObjectMetadata(ctx, "/bucket/type.txt", map[string]string{
		"X-Amz-Metadata-Directive": "REPLACE",
		"Content-Type":             "application/json",
	}); err != nil {
		t.Fatalf("self-copy failed: %v", err)
	}

	stored, ok := b.loadObjectMetadata(ctx, "/bucket/type.txt")
	if !ok {
		t.Fatalf("metadata row disappeared")
	}
	if got := stored["Content-Type"]; got != "application/json" {
		t.Fatalf("Content-Type = %q, want the value the REPLACE request asked for", got)
	}
	// REPLACE is wholesale: the user metadata the request did not mention goes.
	if _, present := stored["x-amz-meta-pin"]; present {
		t.Fatalf("REPLACE kept user metadata the request did not mention: %v", stored)
	}
}

// The `?cors` branch answers the request itself instead of calling next, so a
// guard placed further in never ran for it: a PUT whose declared payload hash
// did not describe the document on the wire was stored anyway. The guard has to
// wrap the CORS branch, and this drives the chain NewServer actually builds.
func TestCORSSubresourcePayloadHashIsVerified(t *testing.T) {
	withCORSTestFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	handler, err := NewServer(ctx)
	require.NoError(t, err)

	document := `<CORSConfiguration><CORSRule>` +
		`<AllowedOrigin>https://attacker.example</AllowedOrigin>` +
		`<AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`
	req := httptest.NewRequest(http.MethodPut, "/"+corsTestBucket+"?cors", strings.NewReader(document))
	// The hash of some other payload: what a request rewritten in transit looks
	// like.
	req.Header.Set("X-Amz-Content-Sha256", strings.Repeat("0", 64))

	recorder := doCORSRequest(t, handler, req)

	assert.NotEqual(t, http.StatusOK, recorder.Code, "a payload hash that does not describe the bytes must be refused")
	_, stored := getS3CORSConfiguration()[corsTestBucket]
	assert.False(t, stored, "the document must not have been stored: %s", recorder.Body.String())
}

// TestCompleteMultipartRejectsContentForDirectoryKey pins that a completion of
// an upload whose key ends in "/" cannot drop the uploaded bytes on the floor.
//
// putStream takes such a key for a directory marker and stores nothing, and it
// only refuses content when it is told the size - which a completion cannot do,
// because the request body is the XML part list. The completion used to answer
// success with a composite ETag for bytes that were never written: the client
// believed it had published the object, a read of the key returned an empty
// object with no ETag, and an empty folder was left where the data should have
// been. PutObject to the same shape of key is refused, so the multipart path
// has to refuse it too.
func TestCompleteMultipartRejectsContentForDirectoryKey(t *testing.T) {
	ctx := context.Background()
	b, root := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "folder/", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}
	e1, err := b.UploadPart(ctx, "mp", "folder/", uploadID, 1, 5, strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("UploadPart 1: %+v", err)
	}
	e2, err := b.UploadPart(ctx, "mp", "folder/", uploadID, 2, 6, strings.NewReader(" world"))
	if err != nil {
		t.Fatalf("UploadPart 2: %+v", err)
	}

	_, etag, err := b.CompleteMultipartUpload(ctx, "mp", "folder/", uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: e1}, {PartNumber: 2, ETag: e2}},
	})
	if err == nil {
		t.Fatalf("completing an upload to %q succeeded with etag %s; the 11 uploaded bytes were dropped", "folder/", etag)
	}
	if code := s3ErrorCode(err); code != gofakes3.ErrInvalidArgument {
		t.Fatalf("completion error = %v (code %v), want 400 InvalidArgument", err, code)
	}

	// Nothing may be published for the key, and no phantom folder may be left
	// behind in its place.
	if _, readErr := b.GetObject(ctx, "mp", "folder/", nil); readErr == nil {
		t.Fatal("a key whose content was refused still answered a read")
	}
	entries, dirErr := os.ReadDir(root)
	if dirErr != nil {
		t.Fatalf("read local root: %+v", dirErr)
	}
	for _, entry := range entries {
		t.Fatalf("the refused completion left %q behind in the storage root", entry.Name())
	}

	// A refused completion must not swallow the upload: the client has to be
	// able to abort it instead of waiting for the reaper.
	if _, stillThere := b.uploads.Load(uploadID); !stillThere {
		t.Fatal("the upload vanished after a refused completion")
	}
	if err := b.AbortMultipartUpload(ctx, "mp", "folder/", uploadID); err != nil {
		t.Fatalf("AbortMultipartUpload after a refused completion: %+v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "folder")); err == nil {
		t.Fatal("abort left the folder behind")
	}

	// The plain write path refuses the same key with the same code; the two
	// paths must agree, or a client cannot predict what the gateway does.
	if _, putErr := b.PutObject(ctx, "mp", "folder/", nil, strings.NewReader("hello world"), 11); s3ErrorCode(putErr) != gofakes3.ErrInvalidArgument {
		t.Fatalf("PutObject to %q = %v (code %v), want InvalidArgument", "folder/", putErr, s3ErrorCode(putErr))
	}
}

// TestCompleteMultipartEmptyPartsForDirectoryKeyIsMarker pins the other side of
// the rule above: an upload that carries no bytes at all is exactly the
// directory marker a client creates on purpose, and it must still be publishable.
func TestCompleteMultipartEmptyPartsForDirectoryKeyIsMarker(t *testing.T) {
	ctx := context.Background()
	b, root := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "marker/", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}
	e1, err := b.UploadPart(ctx, "mp", "marker/", uploadID, 1, 0, strings.NewReader(""))
	if err != nil {
		t.Fatalf("UploadPart: %+v", err)
	}
	if _, _, err := b.CompleteMultipartUpload(ctx, "mp", "marker/", uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: e1}},
	}); err != nil {
		t.Fatalf("completing an empty upload to a directory key: %+v", err)
	}
	info, statErr := os.Stat(filepath.Join(root, "marker"))
	if statErr != nil || !info.IsDir() {
		t.Fatalf("the marker directory was not created: %+v", statErr)
	}
	if _, present := b.uploads.Load(uploadID); present {
		t.Fatal("the completed upload was not dropped")
	}
}
