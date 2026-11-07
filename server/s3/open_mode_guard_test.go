package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

// openModeChain builds the real middleware chain NewServer builds, with no
// credentials configured. That is upstream's documented default for a gateway
// that never set up S3, and it is the one mode in which the authentication
// middleware returns before it looks at the request at all.
func openModeChain(t *testing.T) http.Handler {
	t.Helper()
	setupMultipartBackend(t)
	setTestSetting(conf.S3BucketAuth, "[]")
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	setTestSetting(conf.S3BucketCors, "")
	t.Cleanup(func() {
		setTestSetting(conf.S3Buckets, "[]")
		setTestSetting(conf.S3BucketAuth, "[]")
		setTestSetting(conf.S3BucketCors, "")
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	handler, err := NewServer(ctx)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return handler
}

// openModeRequest runs one request through the chain. The Content-Length header
// is set explicitly: a real server populates it from the request line, while
// httptest.NewRequest only fills in r.ContentLength.
func openModeRequest(handler http.Handler, method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	if body != nil {
		if sizer, ok := body.(interface{ Len() int }); ok {
			req.Header.Set("Content-Length", fmt.Sprint(sizer.Len()))
		}
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestCopySourceWithoutKeyIsRefused pins that a copy source that names no key is
// answered with 400 InvalidArgument instead of reaching the storage layer.
//
// Every consumer of X-Amz-Copy-Source splits the header into a bucket and a key
// and then indexes the second half: gofakes3's copyObject does
// `parts := strings.SplitN(strings.TrimPrefix(source, "/"), "/", 2)` followed by
// `parts[1]`, which panics for a source without a "/" ("", "mp",
// "mp?versionId=1"). bucketAuthHandler refuses exactly those shapes, but only
// once an access-key rule matched; with no credentials configured it hands the
// request straight to gofakes3, so an unauthenticated client could turn any copy
// into a panic (HTTP 500 plus a stack trace in the log) instead of a 400.
func TestCopySourceWithoutKeyIsRefused(t *testing.T) {
	handler := openModeChain(t)

	if rec := openModeRequest(handler, http.MethodPut, "/mp/src.txt", strings.NewReader("hello"), nil); rec.Code != http.StatusOK {
		t.Fatalf("seeding the source object: %d %s", rec.Code, rec.Body.String())
	}

	for _, source := range []string{"", "mp", "/mp", "mp?versionId=1"} {
		rec := openModeRequest(handler, http.MethodPut, "/mp/dst.txt", nil,
			map[string]string{"X-Amz-Copy-Source": source})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("copy source %q: status %d, want 400 (body %s)", source, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "InvalidArgument") {
			t.Fatalf("copy source %q: body %q, want InvalidArgument", source, rec.Body.String())
		}
	}

	// A source that does name both halves still copies: the check must not
	// swallow the ordinary shapes.
	rec := openModeRequest(handler, http.MethodPut, "/mp/dst.txt", nil,
		map[string]string{"X-Amz-Copy-Source": "/mp/src.txt"})
	if rec.Code != http.StatusOK {
		t.Fatalf("well-formed copy: %d %s", rec.Code, rec.Body.String())
	}
	rec = openModeRequest(handler, http.MethodGet, "/mp/dst.txt", nil, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello" {
		t.Fatalf("copied object: %d %q", rec.Code, rec.Body.String())
	}
}

// TestOpenModeGuardsAnswerBeforeTheHandler pins which guards still run when no
// credentials are configured. bucketAuthHandler returns to next immediately in
// that mode, so everything that must not depend on authentication has to sit
// outside it — this test is the regression net for that ordering: a guard that
// silently moves below the authentication middleware, or behind a branch that
// answers without calling next, stops firing here.
func TestOpenModeGuardsAnswerBeforeTheHandler(t *testing.T) {
	handler := openModeChain(t)

	if rec := openModeRequest(handler, http.MethodPut, "/mp/obj.txt", strings.NewReader("hello"), nil); rec.Code != http.StatusOK {
		t.Fatalf("seeding: %d %s", rec.Code, rec.Body.String())
	}
	if rec := openModeRequest(handler, http.MethodPut, "/mp/meta.txt", strings.NewReader("x"),
		map[string]string{"X-Amz-Meta-Team": "alpha", "X-Amz-Meta-MixedCase": "beta"}); rec.Code != http.StatusOK {
		t.Fatalf("seeding metadata: %d %s", rec.Code, rec.Body.String())
	}

	cases := []struct {
		name     string
		method   string
		target   string
		body     string
		headers  map[string]string
		wantCode int
		wantBody string
	}{
		// presigned ceilings: SigV4 X-Amz-Expires and SigV2 Expires.
		{"v4 lifetime ceiling", http.MethodGet, "/mp/obj.txt?X-Amz-Expires=9999999", "", nil, 403, "X-Amz-Expires"},
		{"v4 negative lifetime", http.MethodGet, "/mp/obj.txt?X-Amz-Expires=-1", "", nil, 400, "AuthorizationQueryParametersError"},
		{"v2 lifetime ceiling", http.MethodGet, "/mp/obj.txt?Expires=4000000000", "", nil, 403, "AccessDenied"},
		{"v2 lifetime ceiling with credentials", http.MethodGet,
			"/mp/obj.txt?AWSAccessKeyId=k&Expires=4000000000&Signature=s", "", nil, 403, "AccessDenied"},
		{"future X-Amz-Date", http.MethodGet, "/mp/obj.txt?X-Amz-Date=20301231T000000Z", "", nil, 403, "too far in the future"},

		// payload hash verification.
		{"wrong payload hash", http.MethodPut, "/mp/bad.txt", "hello",
			map[string]string{"X-Amz-Content-Sha256": strings.Repeat("0", 64)}, 400, "BadDigest"},
		{"unsigned payload is accepted", http.MethodPut, "/mp/unsigned.txt", "hello",
			map[string]string{"X-Amz-Content-Sha256": "UNSIGNED-PAYLOAD"}, 200, ""},
		{"unknown streaming form", http.MethodPut, "/mp/stream.txt", "hello",
			map[string]string{"X-Amz-Content-Sha256": "STREAMING-BOGUS", "Content-Encoding": "aws-chunked"}, 400, "NotImplemented"},

		// storage class and unwritable headers.
		{"unknown storage class", http.MethodPut, "/mp/obj.txt", "x",
			map[string]string{"X-Amz-Storage-Class": "BOGUS"}, 400, "InvalidStorageClass"},
		{"unsupported acl", http.MethodPut, "/mp/obj.txt", "x",
			map[string]string{"X-Amz-Acl": "public-read"}, 501, "NotImplemented"},
		{"tagging", http.MethodPut, "/mp/obj.txt", "x",
			map[string]string{"X-Amz-Tagging": "a=b"}, 501, "NotImplemented"},
		{"object lock", http.MethodPut, "/mp/obj.txt", "x",
			map[string]string{"X-Amz-Object-Lock-Mode": "GOVERNANCE"}, 501, "NotImplemented"},

		// part number bounds and listing validation. A partNumber without an
		// uploadId is a malformed request, which S3 answers 400 InvalidArgument
		// (it used to be 501 here because the part read was simply unimplemented).
		{"partNumber", http.MethodGet, "/mp/obj.txt?partNumber=1", "", nil, 400, "InvalidArgument"},
		{"list-type", http.MethodGet, "/mp?list-type=3", "", nil, 400, "InvalidArgument"},
		{"max-keys", http.MethodGet, "/mp?max-keys=abc", "", nil, 400, "InvalidArgument"},

		// object key validation.
		{"dotdot path", http.MethodGet, "/mp/../x", "", nil, 400, "InvalidArgument"},
		{"encoded dotdot path", http.MethodGet, "/mp/%2e%2e/x", "", nil, 400, "InvalidArgument"},
		{"dotdot key", http.MethodPut, "/mp/a/../b", "x", nil, 400, "InvalidArgument"},
		{"backslash key", http.MethodPut, "/mp/a%5Cb", "x", nil, 400, "InvalidArgument"},
		{"NUL key", http.MethodPut, "/mp/a%00b", "x", nil, 400, "InvalidArgument"},
		{"reserved temporary key", http.MethodPut, "/mp/" + s3TempKeyPrefix + "deadbeef", "x", nil, 400, "InvalidArgument"},
		{"reserved temporary key read", http.MethodGet, "/mp/" + s3TempKeyPrefix + "deadbeef", "", nil, 400, "InvalidArgument"},
		{"reserved temporary copy source", http.MethodPut, "/mp/obj.txt", "",
			map[string]string{"X-Amz-Copy-Source": "/mp/" + s3TempKeyPrefix + "deadbeef"}, 400, "InvalidArgument"},

		// the multi-delete body limit.
		{"oversized multi-delete body", http.MethodPost, "/mp?delete",
			"<Delete><Object><Key>" + strings.Repeat("a", 2<<20) + "</Key></Object></Delete>", nil, 400, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			rec := openModeRequest(handler, tc.method, tc.target, body, tc.headers)
			if rec.Code != tc.wantCode {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("body %q, want it to contain %q", rec.Body.String(), tc.wantBody)
			}
		})
	}

	// User metadata is lowered so that a stored key cannot carry a case variant
	// a client would not find again.
	rec := openModeRequest(handler, http.MethodHead, "/mp/meta.txt", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD metadata: %d", rec.Code)
	}
	var metaKeys []string
	for key := range rec.Header() {
		if strings.HasPrefix(strings.ToLower(key), "x-amz-meta-") {
			metaKeys = append(metaKeys, key)
		}
	}
	if len(metaKeys) != 2 {
		t.Fatalf("metadata response headers = %v, want the two stored keys", metaKeys)
	}
	for _, key := range metaKeys {
		if strings.ToLower(key) != key {
			t.Fatalf("metadata header %q was not lowered", key)
		}
	}
	// Header.Get canonicalises the name it looks for, which is exactly what the
	// rewritten (all-lowercase) key is not, so read the map.
	found := false
	for key, values := range rec.Header() {
		if strings.EqualFold(key, "X-Amz-Meta-Team") && len(values) == 1 && values[0] == "alpha" {
			found = true
		}
	}
	if !found {
		t.Fatalf("metadata value lost: %v", rec.Header())
	}
}
