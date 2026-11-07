package s3

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHeadDoesNotAdvertiseTheChecksumUnlessAsked covers the shape that made the
// round 22 fix ineffective for HEAD: gofakes3 sets the stored checksum while
// building the response and net/http commits a HEAD answer after the handler
// returns, without either WriteHeader or Write being called on the wrapper, so
// dropping the header only on those two events left it in place.
func TestHeadDoesNotAdvertiseTheChecksumUnlessAsked(t *testing.T) {
	// A HEAD-shaped handler: headers only, no body, no explicit status.
	headOnly := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amz-Checksum-Crc32", "4mqeGQ==")
		w.Header().Set("X-Amz-Sdk-Checksum-Algorithm", "CRC32")
		w.Header().Set("ETag", `"abc"`)
	})
	guard := stripUnrequestedChecksums(headOnly)

	recorder := httptest.NewRecorder()
	guard.ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, "/bucket/obj", nil))
	assert.Empty(t, recorder.Header().Get("X-Amz-Checksum-Crc32"),
		"a plain HEAD must not advertise the object checksum")
	assert.Empty(t, recorder.Header().Get("X-Amz-Sdk-Checksum-Algorithm"),
		"the upload algorithm is never a GET/HEAD response header")
	assert.Equal(t, `"abc"`, recorder.Header().Get("ETag"))

	asked := httptest.NewRequest(http.MethodHead, "/bucket/obj", nil)
	asked.Header.Set("X-Amz-Checksum-Mode", "ENABLED")
	recorder = httptest.NewRecorder()
	guard.ServeHTTP(recorder, asked)
	assert.Equal(t, "4mqeGQ==", recorder.Header().Get("X-Amz-Checksum-Crc32"),
		"an explicit checksum mode still gets the value")
	assert.Empty(t, recorder.Header().Get("X-Amz-Sdk-Checksum-Algorithm"),
		"the upload algorithm is dropped even when the checksum itself is asked for")

	// A write path still drops the header before the body commits.
	written := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amz-Checksum-Crc32", "4mqeGQ==")
		_, _ = w.Write([]byte("body"))
	})
	recorder = httptest.NewRecorder()
	stripUnrequestedChecksums(written).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/bucket/obj", nil))
	assert.Empty(t, recorder.Header().Get("X-Amz-Checksum-Crc32"))
}

// TestSkewedRequestIsRejectedAsS3Does covers the two shapes gofakes3 answered
// inconsistently: a date in the past came back as 400 AccessDenied and a date in
// the future as 403 AccessDenied, so a client could not tell a wrong clock from
// a wrong signature. S3 answers both with 403 RequestTimeTooSkewed.
func TestSkewedRequestIsRejectedAsS3Does(t *testing.T) {
	reached := false
	guard := presignedLifetimeGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	signed := func(offset time.Duration) *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/bucket/obj", nil)
		request.Header.Set("Authorization",
			"AWS4-HMAC-SHA256 Credential=ak/20261029/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=x")
		request.Header.Set("X-Amz-Date", time.Now().UTC().Add(offset).Format("20060102T150405Z"))
		return request
	}

	for _, offset := range []time.Duration{-20 * time.Minute, -61 * time.Minute, 20 * time.Minute} {
		recorder := httptest.NewRecorder()
		guard.ServeHTTP(recorder, signed(offset))
		require.Equal(t, http.StatusForbidden, recorder.Code, "offset %s", offset)
		assert.Contains(t, recorder.Body.String(), "RequestTimeTooSkewed", "offset %s", offset)
	}
	assert.False(t, reached, "a request outside the window must not reach the handler")

	recorder := httptest.NewRecorder()
	guard.ServeHTTP(recorder, signed(-10*time.Minute))
	assert.True(t, reached, "ten minutes of skew is inside the fifteen minute window")

	// A presigned link is bounded by its lifetime, not by the request date: a
	// link signed an hour ago with a two hour lifetime is still valid.
	reached = false
	query := "?X-Amz-Signature=abc&X-Amz-Date=" +
		time.Now().UTC().Add(-time.Hour).Format("20060102T150405Z") + "&X-Amz-Expires=7200"
	guard.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/bucket/obj"+query, nil))
	assert.True(t, reached, "an old date with a lifetime that has not run out stays valid")
}

// TestPresignedLinkExpiredSecondsAgoIsRefused covers the missing edge of the
// round 19 fix: the expiry comparison carried the fifteen minute clock-skew
// allowance, so a link that expired less than fifteen minutes ago fell through
// to gofakes3, which answers 400 with a message about clock differences.
func TestPresignedLinkExpiredSecondsAgoIsRefused(t *testing.T) {
	guard := presignedLifetimeGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("an expired presigned URL reached the handler")
	}))

	issued := time.Now().UTC().Add(-10 * time.Second).Format("20060102T150405Z")
	recorder := httptest.NewRecorder()
	guard.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/bucket/obj?X-Amz-Signature=abc&X-Amz-Date="+issued+"&X-Amz-Expires=5", nil))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "Request has expired")
	assert.NotContains(t, recorder.Body.String(), "difference between the request time")

	// One second of life left still works.
	reached := false
	guard = presignedLifetimeGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	guard.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet,
		"/bucket/obj?X-Amz-Signature=abc&X-Amz-Date="+issued+"&X-Amz-Expires=600", nil))
	assert.True(t, reached)
}

// TestChunkedTrailerChecksumIsEchoedAndStored pins that the checksum a streaming
// upload declares in its trailer survives the request.
//
// The gateway verified the trailer and then dropped it: the PUT answer carried no
// x-amz-checksum-* header, and a later HEAD with ChecksumMode=ENABLED answered
// none either, so a client that asked for its own integrity value to be honoured
// had no way to confirm the object carried it - botocore reads that header off
// the PUT answer to compare against what it computed.
func TestChunkedTrailerChecksumIsEchoedAndStored(t *testing.T) {
	handler, _, _ := sizeChain(t)
	const payload = "the bytes a streaming upload sends with a trailing checksum"
	digest := sha256Base64(payload)

	rec := openModeRequest(handler, http.MethodPut, "/mp/trailer-echo.bin",
		strings.NewReader(trailerFrames(payload, 6, map[string]string{
			"x-amz-checksum-sha256": digest,
		})), trailerHeaders(len(payload), "x-amz-checksum-sha256"))
	require.Equal(t, http.StatusOK, rec.Code, "a body that matches its trailer was refused: %s", rec.Body.String())
	if got := rec.Header().Get("x-amz-checksum-sha256"); got != digest {
		t.Fatalf("PUT answer echoed %q, want %q", got, digest)
	}

	head := openModeRequest(handler, http.MethodHead, "/mp/trailer-echo.bin", nil,
		map[string]string{"X-Amz-Checksum-Mode": "ENABLED"})
	require.Equal(t, http.StatusOK, head.Code)
	if got := head.Header().Get("x-amz-checksum-sha256"); got != digest {
		t.Fatalf("HEAD with checksum mode returned %q, want %q", got, digest)
	}

	// The same object without the request: the value is not volunteered.
	plain := openModeRequest(handler, http.MethodHead, "/mp/trailer-echo.bin", nil, nil)
	if got := plain.Header().Get("x-amz-checksum-sha256"); got != "" {
		t.Fatalf("HEAD without checksum mode advertised %q", got)
	}

	// A trailer this gateway cannot verify has no value to store or echo, and
	// must not refuse the body either (see the round-19 case for that half).
	other := openModeRequest(handler, http.MethodPut, "/mp/trailer-echo-crc64.bin",
		strings.NewReader(trailerFrames(payload, 6, map[string]string{
			"x-amz-checksum-crc64nvme": "AAAAAAAAAAA=",
		})), trailerHeaders(len(payload), "x-amz-checksum-crc64nvme"))
	require.Equal(t, http.StatusOK, other.Code, "an unverifiable trailer refused the body: %s", other.Body.String())
	if got := other.Header().Get("x-amz-checksum-crc64nvme"); got != "" {
		t.Fatalf("an unverifiable trailer was echoed back as %q", got)
	}
}
