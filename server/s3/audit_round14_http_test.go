package s3

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

// auditRound14Rules scopes one signing key to a single bucket. Both condition
// guards run in front of bucketAuthHandler, so a caller who may not touch a
// bucket is the interesting case: the guard must stay silent and let the auth
// layer answer, exactly as it already does for an unsigned caller.
const auditRound14Rules = `[{"access_key":"scoped-key","secret_key":"scoped-secret","buckets":["allowed-bucket"],"readonly":false}]`

// signedRequest builds a request signed with the key scoped by
// auditRound14Rules.
func signedRequest(t *testing.T, method, target string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	signV2Request(t, req, "scoped-key", "scoped-secret")
	return req
}

// TestConditionalDeleteDefersToBucketAuthorization pins that a conditional
// DELETE for a bucket the signing key may not write is answered by the auth
// layer. The guard used to resolve the object and reply 404/412 before
// bucketAuthHandler could refuse the request with 403, which turned it into an
// existence oracle for every bucket of the gateway.
func TestConditionalDeleteDefersToBucketAuthorization(t *testing.T) {
	setTestSetting(conf.S3BucketAuth, auditRound14Rules)
	defer setTestSetting(conf.S3BucketAuth, "")

	keys := newKeyRing(map[string]string{"scoped-key": "scoped-secret"})
	existing := func(context.Context, string, string) (string, bool) { return `"abc123"`, true }

	reached := false
	handler := deleteConditionGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}), keys, existing)

	for _, ifMatch := range []string{`"abc123"`, `"other"`} {
		reached = false
		req := signedRequest(t, http.MethodDelete, "/foreign-bucket/a.txt")
		req.Header.Set("If-Match", ifMatch)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if !reached {
			t.Fatalf("If-Match %s on a foreign bucket was answered %d by the guard instead of the auth layer",
				ifMatch, rec.Code)
		}
	}

	// The bucket the key is scoped to keeps the conditional behaviour.
	reached = false
	req := signedRequest(t, http.MethodDelete, "/allowed-bucket/a.txt")
	req.Header.Set("If-Match", `"other"`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if reached || rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("scoped bucket: code=%d reached=%v, want 412 and not reached", rec.Code, reached)
	}
}

// TestCopySourceConditionDefersToBucketAuthorization is the same hole for the
// copy preconditions: the source of a copy is named in a header, and the guard
// read it from storage without asking whether the caller may read that bucket.
func TestCopySourceConditionDefersToBucketAuthorization(t *testing.T) {
	setTestSetting(conf.S3BucketAuth, auditRound14Rules)
	defer setTestSetting(conf.S3BucketAuth, "")

	keys := newKeyRing(map[string]string{"scoped-key": "scoped-secret"})
	existing := func(context.Context, string, string) (string, time.Time, bool) {
		return `"abc123"`, time.Unix(1700000000, 0), true
	}

	reached := false
	handler := copySourceConditionGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}), keys, existing)

	for _, ifMatch := range []string{`"abc123"`, `"other"`} {
		reached = false
		req := signedRequest(t, http.MethodPut, "/allowed-bucket/dst.txt")
		req.Header.Set("X-Amz-Copy-Source", "/foreign-bucket/src.txt")
		req.Header.Set("X-Amz-Copy-Source-If-Match", ifMatch)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if !reached {
			t.Fatalf("copy source if-match %s in a foreign bucket was answered %d by the guard instead of the auth layer",
				ifMatch, rec.Code)
		}
	}

	// A readable source still gets the precondition evaluated.
	reached = false
	req := signedRequest(t, http.MethodPut, "/allowed-bucket/dst.txt")
	req.Header.Set("X-Amz-Copy-Source", "/allowed-bucket/src.txt")
	req.Header.Set("X-Amz-Copy-Source-If-Match", `"other"`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if reached || rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("readable source: code=%d reached=%v, want 412 and not reached", rec.Code, reached)
	}
}
