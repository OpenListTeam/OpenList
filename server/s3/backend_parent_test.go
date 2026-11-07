package s3

import (
	"context"
	"strings"
	"testing"

	"github.com/OpenListTeam/gofakes3"
)

// TestPutObjectUnderObjectParentIsRejected pins the answer for a key whose
// ancestor is an object. A POSIX bucket cannot hold both, and NoSuchKey (what
// the driver's "not a directory" error used to become) told the client the path
// was simply missing, so tools retried a request that could never succeed.
func TestPutObjectUnderObjectParentIsRejected(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	if err := b.putStream(ctx, "mp", "tree", nil, strings.NewReader("x"), 1); err != nil {
		t.Fatalf("put the object that becomes the parent: %v", err)
	}

	err := b.putStream(ctx, "mp", "tree/deep/leaf.txt", nil, strings.NewReader("x"), 1)
	if err == nil {
		t.Fatal("a key under an object parent was accepted")
	}
	if code := s3ErrorCode(err); code != gofakes3.ErrInvalidArgument {
		t.Fatalf("want InvalidArgument for a key under an object, got %v (%v)", code, err)
	}

	// A missing (as opposed to object-valued) prefix must still be created.
	if err := b.putStream(ctx, "mp", "fresh/a/b/c.txt", nil, strings.NewReader("y"), 1); err != nil {
		t.Fatalf("put under a missing prefix: %v", err)
	}
}
