package s3

import (
	"bytes"
	"context"
	"testing"

	"github.com/OpenListTeam/gofakes3"
)

// putTestObject writes a one-byte object through the backend.
func putTestObject(t *testing.T, backend *s3Backend, ctx context.Context, key string) {
	t.Helper()
	if _, err := backend.PutObject(ctx, "mp", key, nil, bytes.NewReader([]byte("x")), 1); err != nil {
		t.Fatalf("put %q: %v", key, err)
	}
}

// listTestKeys returns the keys a listing reports under prefix. A recursive
// listing (no delimiter) never reports the empty-folder placeholder, so callers
// that look for one ask for a folder-style listing instead.
func listTestKeys(t *testing.T, backend *s3Backend, prefix string, folderStyle ...bool) []string {
	t.Helper()
	pfx := &gofakes3.Prefix{HasPrefix: true, Prefix: prefix}
	if len(folderStyle) > 0 && folderStyle[0] {
		pfx = &gofakes3.Prefix{HasPrefix: true, HasDelimiter: true, Prefix: prefix, Delimiter: "/"}
	}
	var keys []string
	page := gofakes3.ListBucketPage{MaxKeys: 1000}
	for {
		result, err := backend.ListBucket(context.Background(), "mp", pfx, page)
		if err != nil {
			t.Fatalf("list %q: %v", prefix, err)
		}
		for _, item := range result.Contents {
			keys = append(keys, item.Key)
		}
		if !result.IsTruncated || result.NextMarker == "" {
			return keys
		}
		page = gofakes3.ListBucketPage{MaxKeys: 1000, Marker: result.NextMarker}
	}
}

// TestDeleteEmptyFolderSentinelRemovesFolder pins the one handle a client has on
// an empty folder: the sentinel key its listing invented. If deleting it did
// nothing, `aws s3 rm --recursive` would enumerate a key that can never be
// removed and never converge.
func TestDeleteEmptyFolderSentinelRemovesFolder(t *testing.T) {
	ctx := context.Background()
	backend, _ := setupMultipartBackend(t)

	putTestObject(t, backend, ctx, "d/x.txt")
	if _, err := backend.DeleteObject(ctx, "mp", "d/x.txt"); err != nil {
		t.Fatalf("delete object: %v", err)
	}
	before := listTestKeys(t, backend, "d/", true)
	if len(before) != 1 || before[0] != "d/"+emptyObjectName {
		t.Fatalf("expected the empty folder sentinel to be listed, got %v", before)
	}

	if _, err := backend.DeleteObject(ctx, "mp", "d/"+emptyObjectName); err != nil {
		t.Fatalf("delete sentinel: %v", err)
	}
	if after := listTestKeys(t, backend, "d/", true); len(after) != 0 {
		t.Fatalf("empty folder still listed as %v", after)
	}
}

// TestDeleteFolderMarkerRemovesEmptyFolder covers the marker a client wrote with
// PUT "folder/": deleting that key removes the folder it stands for.
func TestDeleteFolderMarkerRemovesEmptyFolder(t *testing.T) {
	backend, _ := setupMultipartBackend(t)
	markerCtx := context.WithValue(context.Background(), dirMarkerRequestKey{}, true)

	if _, err := backend.PutObject(markerCtx, "mp", "e/", nil, bytes.NewReader(nil), 0); err != nil {
		t.Fatalf("put folder marker: %v", err)
	}
	if _, err := backend.DeleteObject(markerCtx, "mp", "e/"); err != nil {
		t.Fatalf("delete folder marker: %v", err)
	}
	if keys := listTestKeys(t, backend, "e"); len(keys) != 0 {
		t.Fatalf("folder marker still listed as %v", keys)
	}
}

// TestDeleteFolderKeyKeepsContent guards the other side: a delete must never
// behave like a recursive delete.
func TestDeleteFolderKeyKeepsContent(t *testing.T) {
	ctx := context.Background()
	backend, _ := setupMultipartBackend(t)
	markerCtx := context.WithValue(ctx, dirMarkerRequestKey{}, true)

	putTestObject(t, backend, ctx, "f/x.txt")
	if _, err := backend.DeleteObject(markerCtx, "mp", "f/"); err != nil {
		t.Fatalf("delete folder key: %v", err)
	}
	if _, err := backend.DeleteObject(ctx, "mp", "f/"+emptyObjectName); err != nil {
		t.Fatalf("delete sentinel: %v", err)
	}

	obj, err := backend.GetObject(ctx, "mp", "f/x.txt", nil)
	if err != nil {
		t.Fatalf("content disappeared after deleting the folder key: %v", err)
	}
	if obj.Contents != nil {
		_ = obj.Contents.Close()
	}
	if keys := listTestKeys(t, backend, "f"); len(keys) != 1 || keys[0] != "f/x.txt" {
		t.Fatalf("listing after folder delete = %v, want [f/x.txt]", keys)
	}
}
