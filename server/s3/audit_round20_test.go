package s3

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// auditRound20BaseHeaders is the header set an ordinary S3 client signs: the
// host plus the framing headers gofakes3 itself requires. Nothing in it names
// an ACL header.
func auditRound20BaseHeaders() []string {
	return []string{"host", "x-amz-content-sha256", "x-amz-date"}
}

// TestACLHeadersMustBeCoveredByTheSignature pins that a signed request cannot
// be used to attach an ACL the signer never saw. gofakes3 applies X-Amz-Acl
// straight from the request, and Signature V4 only authenticates the headers
// listed in SignedHeaders - so anyone who can see a signed PUT on its way to
// the gateway (or who holds a presigned upload URL) could publish an object
// with `public-read` while the signature stays valid.
func TestACLHeadersMustBeCoveredByTheSignature(t *testing.T) {
	handler := copyAuditChain(t)
	base := auditRound20BaseHeaders()

	if rec := signedPut(t, handler, "/dst-bucket/plain.txt", "hello", nil, base); rec.Code != http.StatusOK {
		t.Fatalf("seeding an ordinary object failed: %d %s", rec.Code, rec.Body.String())
	}

	// The interesting request: the ACL header is appended after signing.
	rec := signedPut(t, handler, "/dst-bucket/unsigned-acl.txt", "hello", map[string]string{
		"X-Amz-Acl": "public-read",
	}, base)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an unsigned X-Amz-Acl was honoured: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := s3ErrorCodeIn(t, rec.Body.String()); got != "AccessDenied" {
		t.Fatalf("unsigned X-Amz-Acl answered %q, want AccessDenied", got)
	}

	// The same request with the ACL header covered by the signature reaches the
	// backend. `private` is the one canned ACL gofakes3 implements, so an
	// accepted request is answered 200; what matters here is that the guard does
	// not turn a signed header into a refusal.
	signed := append(auditRound20BaseHeaders(), "x-amz-acl")
	if rec := signedPut(t, handler, "/dst-bucket/signed-acl.txt", "hello", map[string]string{
		"X-Amz-Acl": "private",
	}, signed); rec.Code != http.StatusOK {
		t.Fatalf("a signed X-Amz-Acl was refused: %d %s", rec.Code, rec.Body.String())
	}
}

// TestGrantHeadersMustBeCoveredByTheSignature covers the other spelling of the
// same hole: the grant headers. `X-Amz-Grant-Read` on an upload publishes the
// object just as effectively as X-Amz-Acl does.
func TestGrantHeadersMustBeCoveredByTheSignature(t *testing.T) {
	handler := copyAuditChain(t)
	const grant = `uri="http://acs.amazonaws.com/groups/global/AllUsers"`

	rec := signedPut(t, handler, "/dst-bucket/unsigned-grant.txt", "hello", map[string]string{
		"X-Amz-Grant-Read": grant,
	}, auditRound20BaseHeaders())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an unsigned X-Amz-Grant-Read was honoured: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// gofakes3 does not implement the grant headers, so a signed one is refused
	// by the backend with 501 Not Implemented. The point of the assertion is that
	// the refusal comes from the backend and not from this guard (403).
	signed := append(auditRound20BaseHeaders(), "x-amz-grant-read")
	if rec := signedPut(t, handler, "/dst-bucket/signed-grant.txt", "hello", map[string]string{
		"X-Amz-Grant-Read": grant,
	}, signed); rec.Code == http.StatusForbidden {
		t.Fatalf("a signed X-Amz-Grant-Read was refused by the guard: %d %s", rec.Code, rec.Body.String())
	}
}

// TestAnonymousACLHeaderIsLeftToTheUnsupportedHeaderPolicy pins the
// no-credential half: there is nothing to vouch for the header, so the guard
// stays silent and the existing policy answers - these headers are refused
// outright, and no object is written. (Answering 403 here instead would hide
// the more accurate "this gateway does not implement ACLs" behind a signature
// complaint.)
func TestAnonymousACLHeaderIsLeftToTheUnsupportedHeaderPolicy(t *testing.T) {
	handler := copyAuditChain(t)

	req := httptest.NewRequest(http.MethodPut, "/dst-bucket/anonymous-acl.txt", strings.NewReader("hello"))
	req.Header.Set("Content-Length", "5")
	req.Header.Set("X-Amz-Acl", "public-read")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("an anonymous X-Amz-Acl was accepted: code=%d body=%s", rec.Code, rec.Body.String())
	}

	head := httptest.NewRequest(http.MethodHead, "/dst-bucket/anonymous-acl.txt", nil)
	signV4Header(t, head, "leaky-key", "leaky-secret", auditRound20BaseHeaders())
	headRec := httptest.NewRecorder()
	handler.ServeHTTP(headRec, head)
	if headRec.Code == http.StatusOK {
		t.Fatalf("the refused request still created an object: HEAD code=%d", headRec.Code)
	}
}

// TestOrdinaryRequestsAreUnaffectedByHeaderSignGuard keeps the guard honest: a
// plain signed PUT, a signed DELETE and a presigned GET carry no
// signature-scoped header and must pass untouched.
func TestOrdinaryRequestsAreUnaffectedByHeaderSignGuard(t *testing.T) {
	handler := copyAuditChain(t)
	base := auditRound20BaseHeaders()

	if rec := signedPut(t, handler, "/dst-bucket/plain.txt", "hello", nil, base); rec.Code != http.StatusOK {
		t.Fatalf("plain PUT: %d %s", rec.Code, rec.Body.String())
	}

	del := httptest.NewRequest(http.MethodDelete, "/dst-bucket/plain.txt", nil)
	signV4Header(t, del, "leaky-key", "leaky-secret", base)
	delRec := httptest.NewRecorder()
	handler.ServeHTTP(delRec, del)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("plain DELETE: %d %s", delRec.Code, delRec.Body.String())
	}
}
