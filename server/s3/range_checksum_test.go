package s3

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/OpenListTeam/gofakes3"
)

func TestGetObjectRangeOmitsWholeObjectChecksum(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	meta := map[string]string{
		"Content-Type":         "text/plain",
		"X-Amz-Checksum-Crc32": "Dmqgmg==",
		"X-Amz-Meta-Foo":       "bar",
	}
	if _, err := b.PutObject(ctx, "mp", "file.txt", meta, strings.NewReader("hello world"), 11); err != nil {
		t.Fatalf("PutObject: %+v", err)
	}

	get := func(rng *gofakes3.ObjectRangeRequest) map[string]string {
		t.Helper()
		obj, err := b.GetObject(ctx, "mp", "file.txt", rng)
		if err != nil {
			t.Fatalf("GetObject: %+v", err)
		}
		_, _ = io.Copy(io.Discard, obj.Contents)
		_ = obj.Contents.Close()
		return obj.Metadata
	}

	full := get(nil)
	if full["X-Amz-Checksum-Crc32"] != "Dmqgmg==" || full["X-Amz-Meta-Foo"] != "bar" {
		t.Fatalf("full GET metadata = %v, want the stored checksum and user metadata", full)
	}

	part := get(&gofakes3.ObjectRangeRequest{Start: 0, End: 4})
	if _, ok := part["X-Amz-Checksum-Crc32"]; ok {
		t.Fatalf("ranged GET metadata = %v, want no whole-object checksum", part)
	}
	if part["X-Amz-Meta-Foo"] != "bar" {
		t.Fatalf("ranged GET metadata = %v, want the user metadata", part)
	}
}
