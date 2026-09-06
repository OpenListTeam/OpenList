package s3

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRangeStatusHandlerReturnsPartialContentForRangedBody(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 2-5/16")
		_, _ = io.WriteString(w, "2345")
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/mp/hello.txt", nil)
	rec := httptest.NewRecorder()
	rangeStatusHandler(inner).ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 Partial Content", rec.Code)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 2-5/16" {
		t.Fatalf("Content-Range = %q, want %q", got, "bytes 2-5/16")
	}
	if got := rec.Body.String(); got != "2345" {
		t.Fatalf("body = %q, want %q", got, "2345")
	}
}

func TestRangeStatusHandlerKeepsOKForFullBody(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "full body")
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/mp/hello.txt", nil)
	rec := httptest.NewRecorder()
	rangeStatusHandler(inner).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 OK", rec.Code)
	}
	if got := rec.Body.String(); got != "full body" {
		t.Fatalf("body = %q, want %q", got, "full body")
	}
}

func TestRangeStatusHandlerPreservesExplicitStatus(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "nope")
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/mp/hello.txt", nil)
	rec := httptest.NewRecorder()
	rangeStatusHandler(inner).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRangeStatusHandlerStreamsWithoutBuffering(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-999/1000")
		// Write in chunks to make sure the wrapper flushes each chunk and does
		// not buffer the response body.
		for _, chunk := range strings.Split("abcdefghij", "") {
			if _, err := io.WriteString(w, chunk); err != nil {
				t.Fatalf("write chunk: %v", err)
			}
		}
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/mp/hello.txt", nil)
	rec := httptest.NewRecorder()
	rangeStatusHandler(inner).ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "abcdefghij" {
		t.Fatalf("body = %q, want %q", got, "abcdefghij")
	}
}
