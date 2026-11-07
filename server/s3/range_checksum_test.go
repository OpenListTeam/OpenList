package s3

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/OpenListTeam/gofakes3"
)

// A ranged GET delivers only a slice of the object, so replaying the checksum of
// the complete object makes every client that validates flexible checksums
// reject the read. A block-device client such as ZeroFS performs nothing but
// ranged reads, so this case decides whether it can work at all.
func TestPartialResponseDropsWholeObjectChecksum(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	meta := map[string]string{
		"Content-Type":                 "application/octet-stream",
		"X-Amz-Meta-Pin":               "abc",
		"X-Amz-Checksum-Crc32":         "poTHxg==",
		"X-Amz-Sdk-Checksum-Algorithm": "CRC32",
	}
	if _, err := b.PutObject(ctx, "mp", "seg.bin", meta, strings.NewReader("0123456789"), 10); err != nil {
		t.Fatalf("PutObject: %+v", err)
	}

	readObject := func(r *gofakes3.ObjectRangeRequest) *gofakes3.Object {
		t.Helper()
		obj, err := b.GetObject(ctx, "mp", "seg.bin", r)
		if err != nil {
			t.Fatalf("GetObject(range=%v): %+v", r, err)
		}
		if _, err := io.Copy(io.Discard, obj.Contents); err != nil {
			t.Fatalf("read body: %v", err)
		}
		return obj
	}

	full := readObject(nil)
	if _, ok := full.Metadata["X-Amz-Checksum-Crc32"]; !ok {
		t.Fatalf("full GET lost the stored checksum, metadata = %v", full.Metadata)
	}

	partial := readObject(&gofakes3.ObjectRangeRequest{Start: 2, End: 4})
	for _, h := range []string{"X-Amz-Checksum-Crc32", "X-Amz-Sdk-Checksum-Algorithm"} {
		if v, ok := partial.Metadata[h]; ok {
			t.Fatalf("partial GET still returns %s = %q, clients cannot validate it", h, v)
		}
	}
	// User metadata and the content type must survive the filter.
	if got := partial.Metadata["x-amz-meta-pin"]; got != "abc" {
		t.Fatalf("partial GET dropped user metadata: %v", partial.Metadata)
	}
	if got := partial.Metadata["Content-Type"]; got != "application/octet-stream" {
		t.Fatalf("partial GET dropped Content-Type: %v", partial.Metadata)
	}
}
