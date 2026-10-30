package s3

import (
	"context"
	"testing"

	"github.com/OpenListTeam/gofakes3"
)

func TestCopyObjectMissingSourceReturnsNoSuchKey(t *testing.T) {
	b, _ := setupMultipartBackend(t)

	_, err := b.CopyObject(context.Background(), "mp", "missing.txt", "mp", "copy.txt", nil)
	if code := s3ErrorCode(err); code != gofakes3.ErrNoSuchKey {
		t.Fatalf("CopyObject() error = %v, want NoSuchKey", code)
	}
}
