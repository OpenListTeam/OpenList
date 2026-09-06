package s3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRangeRequestEndToEndPartialContent drives a real GoFakeS3 server backed
// by a Local storage and verifies that a satisfiable Range GET is answered
// with 206 Partial Content and only the requested byte slice, matching the AWS
// S3 contract. gofakes3 only emits the Content-Range header; the status is
// promoted to 206 by rangeStatusHandler wired into NewServer.
func TestRangeRequestEndToEndPartialContent(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	// Seed a 16-byte object in the "mp" bucket.
	body := []byte("0123456789abcdef")
	if err := b.putStream(ctx, "mp", "hello.txt", map[string]string{"Content-Type": "text/plain"},
		strings.NewReader(string(body)), int64(len(body))); err != nil {
		t.Fatalf("putStream: %+v", err)
	}

	handler, err := NewServer(ctx)
	if err != nil {
		t.Fatalf("NewServer: %+v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://s3.local/mp/hello.txt", nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 Partial Content (body=%q, c-r=%q)",
			rec.Code, rec.Body.String(), rec.Header().Get("Content-Range"))
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 2-5/16" {
		t.Fatalf("Content-Range = %q, want %q", got, "bytes 2-5/16")
	}
	if got := rec.Body.String(); got != "2345" {
		t.Fatalf("body = %q, want %q", got, "2345")
	}
}

// TestRangeRequestEndToEndFullBodyWithoutRange ensures an ordinary GET without
// a Range header still returns 200 with the full body.
func TestRangeRequestEndToEndFullBodyWithoutRange(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	body := []byte("0123456789abcdef")
	if err := b.putStream(ctx, "mp", "hello.txt", map[string]string{"Content-Type": "text/plain"},
		strings.NewReader(string(body)), int64(len(body))); err != nil {
		t.Fatalf("putStream: %+v", err)
	}

	handler, err := NewServer(ctx)
	if err != nil {
		t.Fatalf("NewServer: %+v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://s3.local/mp/hello.txt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 OK", rec.Code)
	}
	got, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
}
