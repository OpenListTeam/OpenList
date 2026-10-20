package s3

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRangeStatusHandler(t *testing.T) {
	for _, tt := range []struct {
		name         string
		method       string
		rangeHeader  string
		contentRange string
		status       int
		body         string
		want         int
	}{
		{"partial", "GET", "bytes=2-5", "bytes 2-5/16", 0, "2345", 206},
		{"explicit OK", "GET", "bytes=2-5", "bytes 2-5/16", 200, "2345", 206},
		{"already partial", "GET", "bytes=2-5", "bytes 2-5/16", 206, "2345", 206},
		{"full", "GET", "", "", 0, "0123456789abcdef", 200},
		{"range ignored", "GET", "bytes=2-5", "", 0, "0123456789abcdef", 200},
		{"not modified", "GET", "bytes=2-5", "", 304, "", 304},
		{"invalid range", "GET", "bytes=20-", "bytes */16", 416, "error", 416},
		{"forbidden", "GET", "bytes=2-5", "", 403, "error", 403},
		{"empty body", "GET", "bytes=2-5", "bytes 2-5/16", 0, "", 206},
		{"head", "HEAD", "bytes=2-5", "", 0, "", 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.contentRange != "" {
					w.Header().Set("Content-Range", tt.contentRange)
				}
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				if tt.body != "" {
					_, _ = io.WriteString(w, tt.body)
				}
			})
			r := httptest.NewRequest(tt.method, "/bucket/object", nil)
			r.Header.Set("Range", tt.rangeHeader)
			w := httptest.NewRecorder()
			rangeStatusHandler(next).ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
			if w.Body.String() != tt.body || w.Header().Get("Content-Range") != tt.contentRange {
				t.Fatalf("response changed: body=%q, range=%q", w.Body.String(), w.Header().Get("Content-Range"))
			}
		})
	}
}
