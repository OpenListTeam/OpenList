package s3

import "net/http"

// gofakes3 sets Content-Range without selecting the partial-content status.
func rangeStatusHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Range") == "" {
			next.ServeHTTP(w, r)
			return
		}
		rw := &rangeResponseWriter{ResponseWriter: w}
		next.ServeHTTP(rw, r)
		if !rw.wroteHeader {
			rw.WriteHeader(http.StatusOK)
		}
	})
}

type rangeResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *rangeResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	if status >= 200 {
		w.wroteHeader = true
	}
	if status == http.StatusOK && w.Header().Get("Content-Range") != "" {
		status = http.StatusPartialContent
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *rangeResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *rangeResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
