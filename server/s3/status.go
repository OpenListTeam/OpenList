// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"net/http"
)

// rangeStatusResponseWriter promotes an implicit 200 to 206 Partial Content
// when the wrapped handler streams a ranged body (signalled by a Content-Range
// response header) without writing an explicit status.
//
// gofakes3 writes the Content-Range and Content-Length headers for a
// satisfiable Range request but never calls WriteHeader, so net/http would
// otherwise send 200 OK. AWS S3 returns 206 Partial Content instead, and some
// clients depend on that status code.
type rangeStatusResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

// WriteHeader forwards an explicit status code unchanged. Any response that
// explicitly chooses its status (errors, redirects, no-content) is left alone.
func (w *rangeStatusResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

// Write triggers the implicit status on the first body write: 206 when the
// response carries a Content-Range header, 200 otherwise.
func (w *rangeStatusResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		if w.Header().Get("Content-Range") != "" {
			w.wroteHeader = true
			w.ResponseWriter.WriteHeader(http.StatusPartialContent)
		} else {
			w.wroteHeader = true
			w.ResponseWriter.WriteHeader(http.StatusOK)
		}
	}
	return w.ResponseWriter.Write(p)
}

// rangeStatusHandler wraps next with rangeStatusResponseWriter so that ranged
// object downloads are answered with 206 Partial Content.
func rangeStatusHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&rangeStatusResponseWriter{ResponseWriter: w}, r)
	})
}
