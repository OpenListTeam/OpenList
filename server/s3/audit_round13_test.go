package s3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

// probeGuard runs one request through a middleware and reports the status, the
// body and whether the wrapped handler was reached.
func probeGuard(t *testing.T, middleware func(http.Handler) http.Handler, method, target string, header map[string]string) (int, string, bool) {
	t.Helper()
	reached := false
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	return rec.Code, string(body), reached
}

// TestVersionIdIsRefusedUniformly pins that every verb refuses a version id,
// not just GET and DELETE. HEAD used to answer 200 with the current object and
// a copy source with `?versionId=` was copied as the current object.
func TestVersionIdIsRefusedUniformly(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete} {
		code, body, reached := probeGuard(t, unsupportedSubresources, method, "/logs/a.txt?versionId=bogus", nil)
		if code != http.StatusNotImplemented || reached {
			t.Fatalf("%s ?versionId: code=%d reached=%v, want 501 and not reached", method, code, reached)
		}
		if !strings.Contains(body, "NotImplemented") {
			t.Fatalf("%s ?versionId body = %q", method, body)
		}
	}
	// A copy naming a source version must be refused too.
	code, _, reached := probeGuard(t, unsupportedSubresources, http.MethodPut, "/logs/dst.txt",
		map[string]string{"X-Amz-Copy-Source": "/logs/src.txt?versionId=bogus"})
	if code != http.StatusNotImplemented || reached {
		t.Fatalf("copy with source versionId: code=%d reached=%v, want 501", code, reached)
	}
	// `versionId=null` means "the current object" and stays allowed.
	if code, _, reached := probeGuard(t, unsupportedSubresources, http.MethodGet, "/logs/a.txt?versionId=null", nil); code != http.StatusOK || !reached {
		t.Fatalf("versionId=null: code=%d reached=%v, want 200", code, reached)
	}
}

// TestUnknownStorageClassIsRejected pins that a misspelled storage class is
// refused instead of silently accepted.
func TestUnknownStorageClassIsRejected(t *testing.T) {
	code, body, reached := probeGuard(t, storageClassGuard, http.MethodPut, "/logs/a.txt",
		map[string]string{"X-Amz-Storage-Class": "MADE_UP"})
	if code != http.StatusBadRequest || reached || !strings.Contains(body, "InvalidStorageClass") {
		t.Fatalf("bogus class: code=%d reached=%v body=%q, want 400 InvalidStorageClass", code, reached, body)
	}
	for _, class := range []string{"", "STANDARD", "STANDARD_IA", "INTELLIGENT_TIERING"} {
		header := map[string]string{}
		if class != "" {
			header["X-Amz-Storage-Class"] = class
		}
		if code, _, reached := probeGuard(t, storageClassGuard, http.MethodPut, "/logs/a.txt", header); code != http.StatusOK || !reached {
			t.Fatalf("class %q: code=%d reached=%v, want 200", class, code, reached)
		}
	}
	// Reads are not affected.
	if code, _, reached := probeGuard(t, storageClassGuard, http.MethodGet, "/logs/a.txt",
		map[string]string{"X-Amz-Storage-Class": "MADE_UP"}); code != http.StatusOK || !reached {
		t.Fatalf("GET with bogus class: code=%d reached=%v, want 200", code, reached)
	}
}

// TestWebsiteRedirectLocationIsKept pins that the header survives the metadata
// filter, so HEAD/GET can echo it back.
func TestWebsiteRedirectLocationIsKept(t *testing.T) {
	stored := storableMetadata(map[string]string{"X-Amz-Website-Redirect-Location": "/elsewhere"})
	if stored["X-Amz-Website-Redirect-Location"] != "/elsewhere" {
		t.Fatalf("stored metadata = %v, want the redirect location kept", stored)
	}
}

// TestDeleteWithIfMatchIsConditional pins that a conditional delete really is
// conditional: a mismatched ETag must leave the object alone.
func TestDeleteWithIfMatchIsConditional(t *testing.T) {
	setTestSetting(conf.S3BucketAuth, "")
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	keys := newKeyRing(nil)

	existing := func(context.Context, string, string) (string, bool) { return `"abc123"`, true }
	missing := func(context.Context, string, string) (string, bool) { return "", false }

	cases := []struct {
		name     string
		ifMatch  string
		identity deleteIdentityFunc
		want     int
		reached  bool
	}{
		{"match", `"abc123"`, existing, http.StatusNoContent, true},
		{"match unquoted", "abc123", existing, http.StatusNoContent, true},
		{"match wildcard", "*", existing, http.StatusNoContent, true},
		{"mismatch", `"other"`, existing, http.StatusPreconditionFailed, false},
		{"missing object", `"abc123"`, missing, http.StatusNotFound, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			handler := deleteConditionGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			}), keys, tc.identity)
			req := httptest.NewRequest(http.MethodDelete, "/logs/a.txt", nil)
			req.Header.Set("If-Match", tc.ifMatch)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want || reached != tc.reached {
				t.Fatalf("code=%d reached=%v, want %d / %v", rec.Code, reached, tc.want, tc.reached)
			}
		})
	}
	// Without the header the delete is unconditional and must pass through.
	reached := false
	handler := deleteConditionGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}), keys, existing)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/logs/a.txt", nil))
	if !reached || rec.Code != http.StatusNoContent {
		t.Fatalf("unconditional delete: code=%d reached=%v", rec.Code, reached)
	}
}

// TestUnsupportedWriteHeadersAreRefused pins that headers whose effect the
// gateway cannot provide are refused rather than silently dropped.
func TestUnsupportedWriteHeadersAreRefused(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		header  map[string]string
		want    int
		reached bool
	}{
		{"tagging", http.MethodPut, map[string]string{"X-Amz-Tagging": "a=b"}, http.StatusNotImplemented, false},
		{"public-read acl", http.MethodPut, map[string]string{"X-Amz-Acl": "public-read"}, http.StatusNotImplemented, false},
		{"object lock", http.MethodPut, map[string]string{"X-Amz-Object-Lock-Mode": "GOVERNANCE"}, http.StatusNotImplemented, false},
		{"private acl", http.MethodPut, map[string]string{"X-Amz-Acl": "private"}, http.StatusOK, true},
		{"bucket owner acl", http.MethodPut, map[string]string{"X-Amz-Acl": "bucket-owner-full-control"}, http.StatusOK, true},
		{"no headers", http.MethodPut, nil, http.StatusOK, true},
		{"read with acl header", http.MethodGet, map[string]string{"X-Amz-Acl": "public-read"}, http.StatusOK, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, reached := probeGuard(t, unsupportedWriteHeaderGuard, tc.method, "/logs/a.txt", tc.header)
			if code != tc.want || reached != tc.reached {
				t.Fatalf("code=%d reached=%v, want %d / %v", code, reached, tc.want, tc.reached)
			}
		})
	}
}

// TestCopySourceRefRejectsEncodedSeparators pins that a copy source whose
// separators are percent-encoded is reported as malformed. gofakes3 re-parses
// the raw header with a plain split on "/" and panics on a one-element slice, so
// this shape has to be refused before it gets there.
func TestCopySourceRefRejectsEncodedSeparators(t *testing.T) {
	for _, header := range []string{"logs%2Fdir%2Fobj.txt", "%2Flogs%2Fdir%2Fobj.txt", "logs"} {
		req := httptest.NewRequest(http.MethodPut, "/logs/dst.txt", nil)
		req.Header.Set("X-Amz-Copy-Source", header)
		bucket, key, present := copySourceRef(req)
		if !present || bucket != "" || key != "" {
			t.Fatalf("%q -> (%q,%q,%v), want an empty, present source", header, bucket, key, present)
		}
	}
	// The ordinary shapes still parse.
	for _, tc := range [][3]string{
		{"/logs/dir/obj.txt", "logs", "dir/obj.txt"},
		{"/logs/dir/obj%20with%20space.txt", "logs", "dir/obj with space.txt"},
	} {
		req := httptest.NewRequest(http.MethodPut, "/logs/dst.txt", nil)
		req.Header.Set("X-Amz-Copy-Source", tc[0])
		bucket, key, present := copySourceRef(req)
		if !present || bucket != tc[1] || key != tc[2] {
			t.Fatalf("%q -> (%q,%q,%v)", tc[0], bucket, key, present)
		}
	}
}
