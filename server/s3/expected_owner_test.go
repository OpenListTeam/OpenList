package s3

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveWithOwnerGuard runs one request through expectedBucketOwnerGuard and
// returns the status code plus whether the wrapped handler was reached.
func serveWithOwnerGuard(t *testing.T, method, target string, header map[string]string) (int, bool, string) {
	t.Helper()
	reached := false
	handler := expectedBucketOwnerGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, reached, string(body)
}

// TestExpectedBucketOwnerMismatchIsForbidden pins the safety net: a client that
// names the bucket it expects must not be served a different bucket.
func TestExpectedBucketOwnerMismatchIsForbidden(t *testing.T) {
	code, reached, body := serveWithOwnerGuard(t, http.MethodPut, "/logs/a.txt",
		map[string]string{"X-Amz-Expected-Bucket-Owner": "photos"})
	if code != http.StatusForbidden || reached {
		t.Fatalf("mismatched owner: code=%d reached=%v, want 403 and not reached", code, reached)
	}
	if !strings.Contains(body, "AccessDenied") {
		t.Fatalf("body = %q, want an AccessDenied error", body)
	}
}

// TestExpectedBucketOwnerMatchPasses guards against over-blocking.
func TestExpectedBucketOwnerMatchPasses(t *testing.T) {
	if code, reached, _ := serveWithOwnerGuard(t, http.MethodPut, "/logs/a.txt",
		map[string]string{"X-Amz-Expected-Bucket-Owner": "logs"}); code != http.StatusOK || !reached {
		t.Fatalf("matching owner: code=%d reached=%v, want 200 and reached", code, reached)
	}
	if code, reached, _ := serveWithOwnerGuard(t, http.MethodPut, "/logs/a.txt", nil); code != http.StatusOK || !reached {
		t.Fatalf("absent header: code=%d reached=%v, want 200 and reached", code, reached)
	}
}

// TestExpectedSourceBucketOwnerMismatchIsForbidden covers the copy variant.
func TestExpectedSourceBucketOwnerMismatchIsForbidden(t *testing.T) {
	code, reached, _ := serveWithOwnerGuard(t, http.MethodPut, "/logs/copy.txt", map[string]string{
		"X-Amz-Copy-Source":                  "/photos/a.txt",
		"X-Amz-Expected-Source-Bucket-Owner": "archive",
	})
	if code != http.StatusForbidden || reached {
		t.Fatalf("mismatched source owner: code=%d reached=%v, want 403 and not reached", code, reached)
	}
	if code, reached, _ := serveWithOwnerGuard(t, http.MethodPut, "/logs/copy.txt", map[string]string{
		"X-Amz-Copy-Source":                  "/photos/a.txt",
		"X-Amz-Expected-Source-Bucket-Owner": "photos",
	}); code != http.StatusOK || !reached {
		t.Fatalf("matching source owner: code=%d reached=%v, want 200", code, reached)
	}
}
