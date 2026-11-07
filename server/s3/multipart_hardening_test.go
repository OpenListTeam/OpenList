package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/gofakes3"
)

// TestUploadPartRefusedWhileCompleting pins that a part arriving after a
// completion started is refused instead of replacing a part file the
// completion has validated but not yet opened.
func TestUploadPartRefusedWhileCompleting(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "late.bin", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}
	if _, err := b.UploadPart(ctx, "mp", "late.bin", uploadID, 1, 4, bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("UploadPart: %+v", err)
	}

	val, _ := b.uploads.Load(uploadID)
	state := val.(*multipartState)
	state.mu.Lock()
	state.completing = true
	state.mu.Unlock()

	_, err = b.UploadPart(ctx, "mp", "late.bin", uploadID, 2, 5, bytes.NewReader([]byte("later")))
	if err != gofakes3.ErrNoSuchUpload {
		t.Fatalf("UploadPart during completion = %v, want NoSuchUpload", err)
	}
}

// TestUploadPartRejectsOversizedPart pins the 5 GiB ceiling on one part: the
// check has to run before anything is streamed, so a signed request cannot make
// the gateway fill its temp dir through a single part.
func TestUploadPartRejectsOversizedPart(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "huge.bin", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}
	_, err = b.UploadPart(ctx, "mp", "huge.bin", uploadID, 1, maxUploadPartSize+1, bytes.NewReader(nil))
	if err == nil || !strings.Contains(err.Error(), "EntityTooLarge") {
		t.Fatalf("UploadPart above the part ceiling = %v, want EntityTooLarge", err)
	}
}

// blockingReader withholds its bytes until gate is closed, so a caller can hold
// an UploadPart "in flight" for as long as the test needs.
type blockingReader struct {
	data []byte
	pos  int
	gate chan struct{}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if r.pos == 0 {
		<-r.gate
	}
	if r.pos >= len(r.data) {
		return 0, nil
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// TestReaperKeepsUploadWithPartInFlight pins the race the reaper used to lose:
// an upload idle past its TTL that is now slowly streaming a part must not have
// its temp directory deleted underneath the write.
func TestReaperKeepsUploadWithPartInFlight(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "slow.bin", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}
	body := &blockingReader{data: []byte("payload"), gate: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := b.UploadPart(ctx, "mp", "slow.bin", uploadID, 1, int64(len(body.data)), body)
		done <- err
	}()

	// Give the upload a moment to enter UploadPart - which stamps lastActivity
	// at that point - then reap as a ticker would once the TTL has elapsed
	// during the stream itself. That is the case the old check got wrong: it
	// only knew when the part started, so a part slower than the TTL looked
	// abandoned for its whole duration.
	time.Sleep(50 * time.Millisecond)
	b.reapExpired(time.Now().Add(2*time.Hour), time.Hour)

	close(body.gate)
	if err := <-done; err != nil {
		t.Fatalf("UploadPart while the reaper ran: %+v", err)
	}
	if _, ok := b.uploads.Load(uploadID); !ok {
		t.Fatal("the reaper dropped an upload that was being written to")
	}
}

// TestUploadPartCopy pins UploadPartCopy: a part written from an existing object
// must contain that object's bytes and answer with a CopyPartResult carrying the
// part's etag, which is what every SDK's server-side copy relies on.
func TestUploadPartCopy(t *testing.T) {
	ctx := context.Background()
	b, localRoot := setupMultipartBackend(t)

	source := []byte("0123456789abcdef")
	if _, err := b.PutObject(ctx, "mp", "source.bin", nil, bytes.NewReader(source), int64(len(source))); err != nil {
		t.Fatalf("PutObject source: %+v", err)
	}
	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "copy.bin", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}

	handler := uploadPartCopyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request fell through to gofakes3 instead of being handled as a part copy")
	}), b, openTestKeyRing(t))

	url := "/mp/copy.bin?partNumber=1&uploadId=" + string(uploadID)
	req := httptest.NewRequest(http.MethodPut, url, nil)
	req.Header.Set("X-Amz-Copy-Source", "/mp/source.bin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("UploadPartCopy = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var result uploadPartCopyResult
	if err := xml.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode CopyPartResult: %+v (body %s)", err, rec.Body.String())
	}
	if result.ETag == "" {
		t.Fatal("CopyPartResult has no ETag; the SDK cannot complete the upload")
	}

	val, ok := b.uploads.Load(uploadID)
	if !ok {
		t.Fatal("upload disappeared")
	}
	part := val.(*multipartState).parts[1]
	if part == nil {
		t.Fatal("part 1 was not recorded")
	}
	stored, err := os.ReadFile(part.path)
	if err != nil {
		t.Fatalf("read stored part: %+v", err)
	}
	if !bytes.Equal(stored, source) {
		t.Fatalf("copied part = %q, want %q", stored, source)
	}
	if want := `"` + part.md5hex + `"`; result.ETag != want {
		t.Fatalf("CopyPartResult ETag = %s, want %s", result.ETag, want)
	}

	// A range copies only the requested bytes, as X-Amz-Copy-Source-Range says.
	uploadID, err = b.CreateMultipartUpload(ctx, "mp", "copy-range.bin", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}
	req = httptest.NewRequest(http.MethodPut, "/mp/copy-range.bin?partNumber=1&uploadId="+string(uploadID), nil)
	req.Header.Set("X-Amz-Copy-Source", "/mp/source.bin")
	req.Header.Set("X-Amz-Copy-Source-Range", "bytes=4-7")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ranged UploadPartCopy = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	val, _ = b.uploads.Load(uploadID)
	part = val.(*multipartState).parts[1]
	ranged, err := os.ReadFile(part.path)
	if err != nil {
		t.Fatalf("read ranged part: %+v", err)
	}
	if !bytes.Equal(ranged, source[4:8]) {
		t.Fatalf("ranged part = %q, want %q", ranged, source[4:8])
	}
	_ = localRoot
}

// TestUploadPartCopyMissingSource pins that a copy from a key that does not
// exist reports NoSuchKey instead of storing an empty part and answering 200.
func TestUploadPartCopyMissingSource(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "copy.bin", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}
	handler := uploadPartCopyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request fell through to gofakes3")
	}), b, openTestKeyRing(t))

	req := httptest.NewRequest(http.MethodPut, "/mp/copy.bin?partNumber=1&uploadId="+string(uploadID), nil)
	req.Header.Set("X-Amz-Copy-Source", "/mp/does-not-exist.bin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("copy of a missing source = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	val, _ := b.uploads.Load(uploadID)
	if part := val.(*multipartState).parts[1]; part != nil {
		t.Fatal("a failed copy recorded a part anyway")
	}
}

// openTestKeyRing puts the gateway in upstream's "no credentials configured"
// mode, where a signed request is not required, so a handler that verifies
// signatures still lets an unsigned unit-test request through.
func openTestKeyRing(t *testing.T) *keyRing {
	t.Helper()
	prevAuth := setting.GetStr(conf.S3BucketAuth)
	prevAK := setting.GetStr(conf.S3AccessKeyId)
	prevSK := setting.GetStr(conf.S3SecretAccessKey)
	setTestSetting(conf.S3BucketAuth, "")
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	t.Cleanup(func() {
		setTestSetting(conf.S3BucketAuth, prevAuth)
		setTestSetting(conf.S3AccessKeyId, prevAK)
		setTestSetting(conf.S3SecretAccessKey, prevSK)
	})
	return newKeyRing(nil)
}
