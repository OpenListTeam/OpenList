package s3

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A signed multi-range GET must be answered like the anonymous one: S3 ignores
// the Range header and serves the whole representation. The old guard left a
// signed Range untouched to preserve the canonical request, so gofakes3 saw the
// comma and answered 501 before the backend could apply its whole-object rule.
func TestSignedMultiRangeIsAnsweredWithWholeObject(t *testing.T) {
	handler, _ := suffixRangeChain(t)
	const body = "0123456789"
	put := httptest.NewRequest(http.MethodPut, "/sr/multi.bin", strings.NewReader(body))
	put.ContentLength = int64(len(body))
	put.Header.Set("Content-Length", fmt.Sprint(len(body)))
	signV2Request(t, put, "sr-key", "sr-secret")
	if rec := httptest.NewRecorder(); func() bool {
		handler.ServeHTTP(rec, put)
		return rec.Code != http.StatusOK
	}() {
		t.Fatal("fixture PUT failed")
	}

	get := httptest.NewRequest(http.MethodGet, "/sr/multi.bin", nil)
	get.Header.Set("Range", "bytes=0-1,3-4")
	signV2Request(t, get, "sr-key", "sr-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, get)
	if rec.Code != http.StatusOK || rec.Body.String() != body {
		t.Fatalf("signed multi-range: code=%d body=%q, want 200 %q", rec.Code, rec.Body.String(), body)
	}
	if got := rec.Header().Get("Content-Range"); got != "" {
		t.Fatalf("whole-object answer has Content-Range %q", got)
	}

}

func TestPutPreconditionsPreserveExistingObject(t *testing.T) {
	handler, root := suffixRangeChain(t)
	if rec := signedPutForCondition(t, handler, "/sr/conditional.txt", "old", ""); rec.Code != http.StatusOK {
		t.Fatalf("initial PUT: %d %s", rec.Code, rec.Body.String())
	}

	for _, tc := range []struct {
		name, condition string
		status          int
	}{
		{name: "wildcard on existing", condition: "*", status: http.StatusPreconditionFailed},
		{name: "matching tag", condition: `"9dd4e461268c8034f5c8564e155c67a6"`, status: http.StatusPreconditionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := signedPutForCondition(t, handler, "/sr/conditional.txt", "new", tc.condition)
			if rec.Code != tc.status {
				t.Fatalf("code=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.status)
			}
			got, err := os.ReadFile(filepath.Join(root, "conditional.txt"))
			if err != nil || string(got) != "old" {
				t.Fatalf("failed condition changed object: body=%q err=%v", got, err)
			}
		})
	}

	if rec := signedPutForCondition(t, handler, "/sr/new.txt", "new", "*"); rec.Code != http.StatusOK {
		t.Fatalf("If-None-Match:* on missing key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := signedPutForCondition(t, handler, "/sr/missing.txt", "new", `"anything"`); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("If-Match on missing key: %d %s, want 412", rec.Code, rec.Body.String())
	}
}

func signedPutForCondition(t *testing.T, handler http.Handler, target, body, noneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, target, strings.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Length", fmt.Sprint(len(body)))
	if noneMatch != "" {
		req.Header.Set("If-None-Match", noneMatch)
		if strings.HasPrefix(noneMatch, `"`) {
			req.Header.Del("If-None-Match")
			req.Header.Set("If-Match", noneMatch)
		}
	}
	signV2Request(t, req, "sr-key", "sr-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
