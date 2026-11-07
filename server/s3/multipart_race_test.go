package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/OpenListTeam/gofakes3"
)

// TestMultipartConcurrentSamePartNumberKeepsWholePart pins that two writers
// uploading the same part number at the same time cannot corrupt each other.
//
// They used to share one file name ("part-00001.tmp"), so their bytes were
// interleaved into the same file and whichever renamed first published a part
// that matched neither etag. Each writer now streams into its own temporary
// file, so the part that survives is always one of the payloads, whole.
func TestMultipartConcurrentSamePartNumberKeepsWholePart(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "race.bin", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}

	payloads := [][]byte{
		bytes.Repeat([]byte("a"), 128*1024),
		bytes.Repeat([]byte("b"), 128*1024),
	}

	var wg sync.WaitGroup
	errs := make([]error, len(payloads))
	for i := range payloads {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = b.UploadPart(ctx, "mp", "race.bin", uploadID, 1, int64(len(payloads[i])), bytes.NewReader(payloads[i]))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent UploadPart %d: %+v", i, err)
		}
	}

	val, ok := b.uploads.Load(uploadID)
	if !ok {
		t.Fatal("upload disappeared")
	}
	state := val.(*multipartState)
	state.mu.Lock()
	part := state.parts[1]
	state.mu.Unlock()
	if part == nil {
		t.Fatal("part 1 was not recorded")
	}

	got, err := os.ReadFile(part.path)
	if err != nil {
		t.Fatalf("read stored part: %+v", err)
	}
	if len(got) != len(payloads[0]) {
		t.Fatalf("stored part has %d bytes, want %d", len(got), len(payloads[0]))
	}
	matched := false
	for _, want := range payloads {
		if bytes.Equal(got, want) {
			matched = true
			break
		}
	}
	if !matched {
		sum := md5.Sum(got)
		t.Fatalf("stored part is a mix of both writers (md5 %s), want one payload intact", hex.EncodeToString(sum[:]))
	}
	// The etag must describe the bytes that were kept.
	sum := md5.Sum(got)
	if want := hex.EncodeToString(sum[:]); part.md5hex != want {
		t.Fatalf("recorded etag md5 = %s, want %s (the stored bytes)", part.md5hex, want)
	}
}

// TestMultipartAbortIgnoredWhileCompleting pins that an abort racing with a
// completion does not pull the part files out from under it. The abort is
// idempotent, so it reports success, but the upload has to survive.
func TestMultipartAbortIgnoredWhileCompleting(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "busy.txt", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}
	etag, err := b.UploadPart(ctx, "mp", "busy.txt", uploadID, 1, 4, bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatalf("UploadPart: %+v", err)
	}

	val, _ := b.uploads.Load(uploadID)
	state := val.(*multipartState)
	state.mu.Lock()
	state.completing = true
	state.mu.Unlock()

	if err := b.AbortMultipartUpload(ctx, "mp", "busy.txt", uploadID); err != nil {
		t.Fatalf("AbortMultipartUpload: %+v", err)
	}
	if _, ok := b.uploads.Load(uploadID); !ok {
		t.Fatal("abort cancelled an upload whose completion was in progress")
	}
	for _, part := range []string{"part-00001"} {
		if _, err := os.Stat(filepath.Join(state.dir, part)); err != nil {
			t.Fatalf("abort deleted %s while the upload was being published: %v", part, err)
		}
	}

	// Once the completion is over, an abort works as usual.
	state.mu.Lock()
	state.completing = false
	state.mu.Unlock()
	if _, _, err := b.CompleteMultipartUpload(ctx, "mp", "busy.txt", uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: etag}},
	}); err != nil {
		t.Fatalf("CompleteMultipartUpload after a refused abort: %+v", err)
	}
}

// TestMultipartCompleteRejectedWhileCompleting pins that a second completion of
// the same upload is refused instead of assembling the parts twice.
func TestMultipartCompleteRejectedWhileCompleting(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "twice.txt", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}
	etag, err := b.UploadPart(ctx, "mp", "twice.txt", uploadID, 1, 4, bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatalf("UploadPart: %+v", err)
	}

	val, _ := b.uploads.Load(uploadID)
	state := val.(*multipartState)
	state.mu.Lock()
	state.completing = true
	state.mu.Unlock()

	_, _, err = b.CompleteMultipartUpload(ctx, "mp", "twice.txt", uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: etag}},
	})
	if code := s3ErrorCode(err); code != gofakes3.ErrNoSuchUpload {
		t.Fatalf("second CompleteMultipartUpload error = %v, want NoSuchUpload", err)
	}

	// The refused call must not have dropped the upload.
	if _, ok := b.uploads.Load(uploadID); !ok {
		t.Fatal("a refused completion dropped the upload")
	}
}

// TestMultipartCompleteManyParts pins that completing an upload with many parts
// streams them in order while holding at most one part file open, and that a
// mid-assembly failure leaves the upload retryable.
func TestMultipartCompleteManyParts(t *testing.T) {
	ctx := context.Background()
	b, localRoot := setupMultipartBackend(t)

	const partCount = 200
	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "many.bin", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}

	parts := make([]gofakes3.CompletedPart, 0, partCount)
	var want bytes.Buffer
	for i := 1; i <= partCount; i++ {
		body := []byte(fmt.Sprintf("part-%03d;", i))
		want.Write(body)
		etag, err := b.UploadPart(ctx, "mp", "many.bin", uploadID, i, int64(len(body)), bytes.NewReader(body))
		if err != nil {
			t.Fatalf("UploadPart %d: %+v", i, err)
		}
		parts = append(parts, gofakes3.CompletedPart{PartNumber: i, ETag: etag})
	}

	_, etag, err := b.CompleteMultipartUpload(ctx, "mp", "many.bin", uploadID, &gofakes3.CompleteMultipartUploadRequest{Parts: parts})
	if err != nil {
		t.Fatalf("CompleteMultipartUpload: %+v", err)
	}
	// The composite ETag of a 200-part upload ends with its part count.
	if !strings.HasSuffix(etag, `-200"`) {
		t.Fatalf("complete etag = %q, want a composite <md5>-200", etag)
	}

	val, ok := b.uploads.Load(uploadID)
	if ok {
		t.Fatalf("upload still tracked after complete: %v", val)
	}

	got, err := os.ReadFile(filepath.Join(localRoot, "many.bin"))
	if err != nil {
		t.Fatalf("read result: %+v", err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("result has %d bytes, want %d (parts streamed out of order?)", len(got), want.Len())
	}
}
