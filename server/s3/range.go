package s3

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/gofakes3"
)

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

// ifRangeContextKey carries the If-Range validator from the HTTP layer down to
// the backend.
type ifRangeContextKey struct{}

// withIfRange carries the If-Range header in the request context.
//
// The header cannot simply be dropped when it does not match: it is part of the
// signed headers of a V4 request, so editing it produces 400 SignatureDoesNotMatch
// instead of the whole-object answer S3 wants. The backend therefore decides
// what to do with the parsed range, which is the only thing gofakes3 lets a
// storage layer influence.
func ifRangeHolder(_ *s3Backend, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		withIfRange(r, next).ServeHTTP(w, r)
	})
}

func withIfRange(r *http.Request, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			if ifRange := strings.TrimSpace(r.Header.Get("If-Range")); ifRange != "" {
				r = r.WithContext(context.WithValue(r.Context(), ifRangeContextKey{}, ifRange))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ifRangeFromContext returns the If-Range validator a GET/HEAD carried, if any.
func ifRangeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(ifRangeContextKey{}).(string); ok {
		return value
	}
	return ""
}

// ifRangeMatchesValidator reports whether the validator the client sent still
// describes the stored object. Anything that cannot be verified counts as a
// mismatch: answering with the whole object is always correct, answering with a
// range the client cannot trust is not.
func ifRangeMatchesValidator(ifRange, etag string, modTime time.Time) bool {
	if strings.HasPrefix(ifRange, "W/") {
		// If-Range needs a strong comparison, so a weak tag never matches.
		return false
	}
	if strings.HasPrefix(ifRange, `"`) {
		return etag != "" && strings.TrimSpace(etag) == ifRange
	}
	clientTime, err := http.ParseTime(ifRange)
	if err != nil || modTime.IsZero() {
		return false
	}
	// HTTP dates have a one-second resolution, so compare at that granularity:
	// the sub-second part of the stored time is not something the client saw.
	return clientTime.Unix() == modTime.Unix()
}

// multiRangeContextKey marks a request whose Range header names more than one
// range.
type multiRangeContextKey struct{}

// ignoreRangeFromContext reports whether the handler below must answer with the
// whole object regardless of the Range header.
func ignoreRangeFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	ignore, _ := ctx.Value(multiRangeContextKey{}).(bool)
	return ignore
}

// multiRangeGuard implements what S3 does with a multi-range request: it
// ignores the header and answers with the whole object and 200. gofakes3
// refuses it with 501 NotImplemented (ranges.go:83), which fails a read that
// every S3 client expects to succeed.
//
// The decision travels in the context to the storage layer, exactly like
// If-Range, but gofakes3 parses the header and refuses it before the storage
// layer is called, so the header is folded into a single range that covers the
// whole object. An unsigned/public request can be changed directly.
func multiRangeGuard(next http.Handler, backend *s3Backend, keys *keyRing) http.Handler {
	unsigned := gofakes3.New(backend, gofakes3.WithLogger(logger{}),
		gofakes3.WithoutVersioning(), gofakes3.WithIntegrityCheck(true)).Server()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// target is the handler for this request only. Assigning to the captured
		// next instead rebound the guard for the rest of the process: the first
		// signed multi-range read sent every later request - of any method, in
		// any bucket - to the keyless sibling below, which verifies no signature
		// at all. One such GET therefore opened the whole gateway to anonymous
		// writes and reads, and it stayed open until the process restarted.
		target := next
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			target.ServeHTTP(w, r)
			return
		}
		rawRange := r.Header.Get("Range")
		if !hasMultipleRanges(rawRange) && rangeSyntaxValid(rawRange) {
			target.ServeHTTP(w, r)
			return
		}
		// Range participates in a V4 signature. Authenticate before editing it;
		// the keyless sibling then performs only routing and backend access. It
		// remains inside bucketAuthHandler, so bucket scope is still enforced.
		if rangeHeaderIsSigned(r) {
			if !s3RequestAuthorized(r, keys) {
				target.ServeHTTP(w, r)
				return
			}
			r = r.Clone(r.Context())
			r.Header = r.Header.Clone()
			target = unsigned
		}
		ctx := context.WithValue(r.Context(), multiRangeContextKey{}, true)
		r.Header.Set("Range", "bytes=0-")
		target.ServeHTTP(w, r.WithContext(ctx))
	})
}

// rangeSyntaxValid reports whether Range is a syntactically valid single
// byte range. RFC 9110 says a server MUST ignore an invalid Range field; an
// unsatisfiable but well-formed range (including start > end) still gets 416.
func rangeSyntaxValid(raw string) bool {
	if raw == "" {
		return true
	}
	spec, ok := strings.CutPrefix(raw, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return false
	}
	start, end, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		return false
	}
	start, end = strings.TrimSpace(start), strings.TrimSpace(end)
	if start == "" {
		n, err := strconv.ParseInt(end, 10, 64)
		return err == nil && n >= 0
	}
	if _, err := strconv.ParseUint(start, 10, 63); err != nil {
		return false
	}
	if end == "" {
		return true
	}
	_, err := strconv.ParseUint(end, 10, 63)
	return err == nil
}

// rangeHeaderIsSigned reports whether the request signature covers the Range
// header, either in the SignedHeaders list of a V4 header-signed request or in
// the X-Amz-SignedHeaders of a presigned one.
func rangeHeaderIsSigned(r *http.Request) bool {
	signed := r.URL.Query().Get("X-Amz-SignedHeaders")
	if signed == "" {
		authorization := r.Header.Get("Authorization")
		if index := strings.Index(authorization, "SignedHeaders="); index >= 0 {
			signed = authorization[index+len("SignedHeaders="):]
			if end := strings.IndexByte(signed, ','); end >= 0 {
				signed = signed[:end]
			}
		}
	}
	for _, name := range strings.Split(signed, ";") {
		if strings.EqualFold(strings.TrimSpace(name), "range") {
			return true
		}
	}
	return false
}

// hasMultipleRanges reports whether a Range header value names more than one
// range. A Range value never contains a quoted string, so a comma is enough.
func hasMultipleRanges(raw string) bool {
	return strings.Contains(raw, ",")
}
