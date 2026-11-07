package s3

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// In Round 40 audit: streaming payloads (STREAMING-*) require a valid non-negative
// X-Amz-Decoded-Content-Length header. Omitting this header previously bypassed
// chunkFrameWatcher and chunk signature verification.

func TestAuditRound40_StreamingMissingDecodedLengthReturns411(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	handler := contentSha256Guard(next)

	for _, form := range []string{
		"STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
		"STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER",
		"STREAMING-UNSIGNED-PAYLOAD-TRAILER",
	} {
		reached = false
		r := httptest.NewRequest(http.MethodPut, "/bucket/object.bin", strings.NewReader("dummy"))
		r.Header.Set("X-Amz-Content-Sha256", form)
		// No X-Amz-Decoded-Content-Length provided
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		assert.False(t, reached, "guard must not let streaming request without decoded length pass for %s", form)
		assert.Equal(t, http.StatusLengthRequired, w.Code, "missing decoded length must yield 411 Length Required")
		assert.Contains(t, w.Body.String(), "MissingContentLength")
	}
}

func TestAuditRound40_StreamingInvalidDecodedLengthReturns400(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	handler := contentSha256Guard(next)

	for _, invalid := range []string{"abc", "-1", "-100", "12.34"} {
		reached = false
		r := httptest.NewRequest(http.MethodPut, "/bucket/object.bin", strings.NewReader("dummy"))
		r.Header.Set("X-Amz-Content-Sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD")
		r.Header.Set("X-Amz-Decoded-Content-Length", invalid)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		assert.False(t, reached, "guard must reject invalid decoded length %q", invalid)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "InvalidArgument")
	}
}

func TestAuditRound40_StreamingValidDecodedLengthInstallsWatcher(t *testing.T) {
	reached := false
	var observedWatcher any
	var observedLength int64
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		observedWatcher = r.Context().Value(chunkWatcherKey{})
		observedLength = decodedContentLength(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := contentSha256Guard(next)

	r := httptest.NewRequest(http.MethodPut, "/bucket/object.bin", strings.NewReader("dummy"))
	r.Header.Set("X-Amz-Content-Sha256", "STREAMING-UNSIGNED-PAYLOAD-TRAILER")
	r.Header.Set("X-Amz-Decoded-Content-Length", "42")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.True(t, reached)
	require.NotNil(t, observedWatcher, "chunkWatcherKey must be set in context")
	assert.Equal(t, int64(42), observedLength, "decodedContentLength must match 42")
}
