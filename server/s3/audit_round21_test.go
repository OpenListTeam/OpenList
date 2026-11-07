package s3

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAnonymousResponseOverridesAreRefused pins the S3 rule that
// `?response-content-type=…` (and the other response-* parameters) are only
// honoured on a signed request. Honouring them anonymously let anyone turn a
// stored text/plain object into text/html served from the gateway's own origin
// - a stored-XSS primitive that needs no write permission at all.
func TestAnonymousResponseOverridesAreRefused(t *testing.T) {
	handler := copyAuditChain(t)

	for _, name := range []string{
		"response-content-type",
		"response-content-disposition",
		"response-cache-control",
		"response-content-encoding",
		"response-expires",
		"response-content-language",
	} {
		target := "/src-bucket/anything.txt?" + name + "=text%2Fhtml"
		rec := openModeRequest(handler, http.MethodGet, target, nil, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("anonymous %s answered %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
		if got := s3ErrorCodeIn(t, rec.Body.String()); got != "InvalidRequest" {
			t.Fatalf("anonymous %s answered %q, want InvalidRequest", name, got)
		}
	}

	// The same anonymous read without any override is not this guard's business:
	// it must fall through to the normal authorisation answer, not become a 400.
	rec := openModeRequest(handler, http.MethodGet, "/src-bucket/anything.txt", nil, nil)
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("an anonymous read without overrides was refused as a bad request: %d %s",
			rec.Code, rec.Body.String())
	}
}

// TestSignedResponseOverridesStillWork is the other half: a signed request may
// use the overrides, so the check cannot be satisfied by refusing everyone.
func TestSignedResponseOverridesStillWork(t *testing.T) {
	handler := copyAuditChain(t)

	base := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if rec := signedPut(t, handler, "/src-bucket/page.txt", "<script>alert(1)</script>", nil, base); rec.Code != http.StatusOK {
		t.Fatalf("seeding the object failed: %d %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet,
		"/src-bucket/page.txt?response-content-type=text%2Fhtml&response-content-disposition=inline", nil)
	signV4Header(t, req, "leaky-key", "leaky-secret", base)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("a signed request with response overrides was refused: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("a signed response-content-type was not applied: Content-Type=%q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "inline") {
		t.Fatalf("a signed response-content-disposition was not applied: %q", got)
	}
}

// TestResponseOverridesAreRefusedForSigV2Query covers the second half of the
// same hole. The SigV2 string-to-sign only covers the resource and a fixed
// sub-resource list, so `response-*` cannot be covered however the request was
// signed: appending it to an old presigned link would choose the served
// Content-Type without authorisation. Refusing with the same 400 as the
// anonymous case is what keeps the override inside a signature.
func TestResponseOverridesAreRefusedForSigV2Query(t *testing.T) {
	hit := false
	handler := responseOverrideGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/bucket/obj.txt?AWSAccessKeyId=ak&Expires=1793257786&Signature=a%2Fb%2Bc%3D&response-content-type=text%2Fhtml", nil))
	if hit {
		t.Fatalf("a SigV2 request with an unsigned response-content-type reached the backend")
	}
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "InvalidRequest")

	// The same presigned request without the override must still be let through
	// to the signature check - this guard is not a second authorisation layer.
	hit = false
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/bucket/obj.txt?AWSAccessKeyId=ak&Expires=1793257786&Signature=a%2Fb%2Bc%3D", nil))
	assert.True(t, hit)
}
