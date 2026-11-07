package s3

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two keys that differ only by redundant slashes name the same file. Locking
// "a//b" used to resolve to the same file as "a/b", so the lock keys on the
// resolved path and two writes to the two spellings serialised instead of
// publishing a torn object. The key is now refused outright, which stops the
// aliased write before it reaches storage; the path-keyed lock stays as the
// second layer for any other shape that resolves the same way.
func TestEmptySegmentKeyIsRefusedBeforeItAliases(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	if _, err := b.PutObject(ctx, "mp", "al/p/x.bin", map[string]string{"Content-Type": "text/x-two"},
		bytes.NewBufferString("two-two"), int64(len("two-two"))); err != nil {
		t.Fatalf("seeding the plain key: %v", err)
	}

	_, err := b.PutObject(ctx, "mp", "al/p//x.bin", map[string]string{"Content-Type": "text/x-one"},
		bytes.NewBufferString("one"), int64(len("one")))
	require.Error(t, err, "an aliased key must be refused instead of overwriting the plain one")

	obj, err := b.HeadObject(ctx, "mp", "al/p/x.bin")
	require.NoError(t, err)
	assert.Equal(t, int64(len("two-two")), obj.Size)
	assert.Equal(t, "text/x-two", obj.Metadata["Content-Type"])
}

// The direct-transfer redirect sits above the subresource handler, so it has to
// refuse every subresource request: `PUT /bucket/key?tagging` was otherwise
// forwarded to storage as an upload and the <Tagging> document became the body.
func TestSubresourceRequestsAreNotRedirected(t *testing.T) {
	for _, sub := range []string{"acl", "tagging", "lifecycle", "policy", "retention", "legal-hold", "attributes", "versions", "location", "cors", "delete"} {
		r := httptest.NewRequest(http.MethodPut, "/bucket/key?"+sub, nil)
		assert.True(t, hasNonObjectQuery(r), "?%s must not be treated as an object request", sub)
	}
	r := httptest.NewRequest(http.MethodGet, "/bucket/key", nil)
	assert.False(t, hasNonObjectQuery(r), "a plain object GET must stay redirectable")
}

// `?cors` describes bucket configuration. An object key in the path used to
// return (or overwrite) the bucket's rules and skip every guard behind it.
func TestCORSConfigurationRejectsObjectKeys(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/anybucket/some/key.txt?cors", nil)
	w := httptest.NewRecorder()

	require.True(t, handleS3CORSConfiguration(w, r), "the request must be answered here")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "InvalidArgument")
}

// A response whose content depends on Origin must never be cached without
// keying on it, even when this particular request carried no Origin.
func TestCORSPassthroughKeepsVaryOrigin(t *testing.T) {
	var seen http.Header
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = w.Header().Clone() })
	w := httptest.NewRecorder()
	corsHandler(next).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/bucket/key.txt", nil))

	require.NotNil(t, seen)
	assert.Contains(t, fmt.Sprint(seen.Values("Vary")), "Origin")
}

// Signature Version 2 spells its expiry as an absolute timestamp and gofakes3
// only checks whether it has passed, so the 7-day ceiling had to be enforced
// for V2 links as well.
func TestPresignedLifetimeBoundsSigV2(t *testing.T) {
	served := func(rawQuery string) int {
		called := false
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})
		w := httptest.NewRecorder()
		presignedLifetimeGuard(next).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/bucket/key.txt?"+rawQuery, nil))
		if called {
			return http.StatusOK
		}
		return w.Code
	}

	farFuture := fmt.Sprintf("AWSAccessKeyId=k&Expires=%d&Signature=abc", time.Now().Add(90*24*time.Hour).Unix())
	assert.Equal(t, http.StatusForbidden, served(farFuture),
		"a V2 link valid for months must be refused like a V4 one")

	soon := fmt.Sprintf("AWSAccessKeyId=k&Expires=%d&Signature=abc", time.Now().Add(time.Hour).Unix())
	assert.Equal(t, http.StatusOK, served(soon), "a short-lived V2 link still works")
}
