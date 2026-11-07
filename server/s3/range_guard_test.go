package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OpenListTeam/gofakes3"
)

// writeTestObject puts body at key in the test bucket.
func writeTestObject(t *testing.T, b *s3Backend, bucket, key string, body []byte) {
	t.Helper()
	ctx := context.Background()
	if _, err := b.PutObject(ctx, bucket, key, nil, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("PutObject %s: %+v", key, err)
	}
}

// A stale If-Range validator means the client's partial file is out of date.
// S3 answers such a request with the whole object; answering with the range
// lets a resuming client splice new bytes onto a file whose beginning changed
// and report a corrupt download as a success.
func TestGetObject_StaleIfRangeServesWholeObject(t *testing.T) {
	backend, _ := setupMultipartBackend(t)
	const bucket = "mp"
	body := []byte("0123456789")
	writeTestObject(t, backend, bucket, "stale.txt", body)

	rangeRequest := &gofakes3.ObjectRangeRequest{Start: 2, End: 5}

	ctx := context.WithValue(context.Background(), ifRangeContextKey{}, "Thu, 01 Jan 1970 00:00:00 GMT")
	obj, err := backend.GetObject(ctx, bucket, "stale.txt", rangeRequest)
	if err != nil {
		t.Fatalf("GetObject: %+v", err)
	}
	defer obj.Contents.Close()
	if obj.Range != nil {
		t.Fatalf("stale If-Range was honoured: object came back as range %+v", obj.Range)
	}
	if obj.Size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", obj.Size, len(body))
	}
	got, err := io.ReadAll(obj.Contents)
	if err != nil {
		t.Fatalf("read body: %+v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

// The plain Range request, an absent validator and a validator that matches the
// stored modification time all keep the range.
func TestGetObject_HonouredRange(t *testing.T) {
	backend, _ := setupMultipartBackend(t)
	const bucket = "mp"
	body := []byte("0123456789")
	writeTestObject(t, backend, bucket, "keep.txt", body)

	rangeRequest := &gofakes3.ObjectRangeRequest{Start: 2, End: 5}
	head, err := backend.HeadObject(context.Background(), bucket, "keep.txt")
	if err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}
	lastModified, err := http.ParseTime(head.Metadata["Last-Modified"])
	if err != nil {
		t.Fatalf("parse stored Last-Modified: %+v", err)
	}

	for name, ctx := range map[string]context.Context{
		"no validator": context.Background(),
		"matching date": context.WithValue(context.Background(), ifRangeContextKey{},
			lastModified.Format(http.TimeFormat)),
	} {
		t.Run(name, func(t *testing.T) {
			obj, err := backend.GetObject(ctx, bucket, "keep.txt", rangeRequest)
			if err != nil {
				t.Fatalf("GetObject: %+v", err)
			}
			defer obj.Contents.Close()
			if obj.Range == nil {
				t.Fatal("range was dropped for a validator that still matches")
			}
			if obj.Range.Start != 2 || obj.Range.Length != 4 {
				t.Fatalf("range = %+v, want start 2 length 4", *obj.Range)
			}
			if obj.Size != int64(len(body)) {
				t.Fatalf("size = %d, want the full object size %d", obj.Size, len(body))
			}
			got, err := io.ReadAll(obj.Contents)
			if err != nil {
				t.Fatalf("read body: %+v", err)
			}
			if string(got) != "2345" {
				t.Fatalf("body = %q, want %q", got, "2345")
			}
		})
	}
}

// The middleware has to carry the header, because the header itself cannot be
// touched: it is part of the V4 signature.
func TestWithIfRange_CarriesHeaderIntoContext(t *testing.T) {
	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = ifRangeFromContext(r.Context())
	})
	req := httptest.NewRequest(http.MethodGet, "http://s3.example/b/k", nil)
	req.Header.Set("If-Range", `"abc"`)
	withIfRange(req, next).ServeHTTP(httptest.NewRecorder(), req)
	if got != `"abc"` {
		t.Fatalf("ctx validator = %q, want %q", got, `"abc"`)
	}

	// A request without the header must stay untouched.
	got = "sentinel"
	req = httptest.NewRequest(http.MethodPut, "http://s3.example/b/k", nil)
	withIfRange(req, next).ServeHTTP(httptest.NewRecorder(), req)
	if got != "" {
		t.Fatalf("ctx validator = %q, want empty", got)
	}
}

func TestIfRangeMatchesValidator(t *testing.T) {
	modTime := time.Date(2026, 10, 28, 19, 42, 53, 0, time.UTC)
	sameDate := modTime.Format(http.TimeFormat)
	otherDate := modTime.Add(time.Second).Format(http.TimeFormat)

	cases := []struct {
		name    string
		ifRange string
		etag    string
		want    bool
	}{
		{name: "matching date", ifRange: sameDate, want: true},
		{name: "stale date", ifRange: otherDate, want: false},
		{name: "matching etag", ifRange: `"abc"`, etag: `"abc"`, want: true},
		{name: "stale etag", ifRange: `"abc"`, etag: `"def"`, want: false},
		{name: "etag without stored tag", ifRange: `"abc"`, want: false},
		{name: "weak tag never matches", ifRange: `W/"abc"`, etag: `"abc"`, want: false},
		{name: "unparsable", ifRange: "not-a-date", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ifRangeMatchesValidator(tc.ifRange, tc.etag, modTime); got != tc.want {
				t.Fatalf("matches = %v, want %v", got, tc.want)
			}
		})
	}
	if ifRangeMatchesValidator(sameDate, "", time.Time{}) {
		t.Fatal("a zero modification time must never match")
	}
}

// A Content-MD5 mismatch reaches UploadPart as gofakes3's typed error. It has to
// come back out unwrapped: gofakes3 classifies errors with a type switch, so a
// wrapped code became 500 InternalError and the SDK retried the same bad body.
func TestUploadPartBadDigestStaysTyped(t *testing.T) {
	ctx := context.Background()
	backend, _ := setupMultipartBackend(t)
	const bucket = "mp"
	uploadID, err := backend.CreateMultipartUpload(ctx, bucket, "digest.bin", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %+v", err)
	}

	_, err = backend.UploadPart(ctx, bucket, "digest.bin", uploadID, 1, 8, digestFailingReader{})
	if err == nil {
		t.Fatal("UploadPart accepted a body whose digest failed")
	}
	code, ok := asS3ErrorCode(err)
	if !ok || code != gofakes3.ErrBadDigest {
		t.Fatalf("UploadPart error = %v (%T), want the bare bad-digest code", err, err)
	}
	if status := code.Status(); status != http.StatusBadRequest {
		t.Fatalf("bad digest maps to %d, want 400", status)
	}
}

type digestFailingReader struct{}

func (digestFailingReader) Read([]byte) (int, error) { return 0, gofakes3.ErrBadDigest }

// A copy source range that does not describe any bytes of the source object is
// a range error, not a missing key: reporting NoSuchKey made clients believe the
// source object had been deleted.
func TestIsRangeError(t *testing.T) {
	if !isRangeError(gofakes3.ErrInvalidRange) {
		t.Fatal("bare invalid-range error not recognised")
	}
	if !isRangeError(errors.Join(errors.New("copy part"), gofakes3.ErrInvalidRange)) {
		t.Fatal("wrapped invalid-range error not recognised")
	}
	if isRangeError(gofakes3.ErrNoSuchKey) {
		t.Fatal("ErrNoSuchKey treated as a range error")
	}
}

// A signed multi-range request must not overwrite the closure-captured next handler.
// Doing so substituted the keyless gofakes3 server for all subsequent requests in the
// process, leaving the entire gateway open to unauthenticated access.
func TestMultiRangeGuardDoesNotRebindNext(t *testing.T) {
	backend, _ := setupMultipartBackend(t)
	const (
		bucket = "mp"
		ak     = "test-ak"
		sk     = "test-sk"
	)
	writeTestObject(t, backend, bucket, "obj.txt", []byte("hello world 1234567890"))
	keys := newKeyRing(map[string]string{ak: sk})

	nextCalls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalls++
		w.WriteHeader(http.StatusOK)
	})

	guard := multiRangeGuard(next, backend, keys)

	// 1. Send signed multi-range GET request.
	req1 := httptest.NewRequest(http.MethodGet, "http://s3.example/mp/obj.txt", nil)
	req1.Header.Set("Range", "bytes=0-1,3-4")
	req1.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	req1.Header.Set("X-Amz-Date", time.Now().UTC().Format("20060102T150405Z"))
	signV4Header(t, req1, ak, sk, []string{"host", "range", "x-amz-content-sha256", "x-amz-date"})

	rec1 := httptest.NewRecorder()
	guard.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec1.Code)
	}

	// 2. Send an ordinary request that should be handled by `next`.
	req2 := httptest.NewRequest(http.MethodGet, "http://s3.example/mp/obj.txt", nil)
	rec2 := httptest.NewRecorder()
	guard.ServeHTTP(rec2, req2)

	if nextCalls != 1 {
		t.Fatalf("next handler calls = %d, want 1 (guard rebound next handler for all subsequent requests!)", nextCalls)
	}
}
