package s3

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPreflightRefusesAHeaderNoRuleAllows pins what S3 does with a preflight
// that asks for a request header the matched rule does not allow: none of the
// response CORS headers are returned. The gateway used to answer with
// Access-Control-Allow-Origin and -Methods and simply omit -Headers, so a
// browser refused the request while any client that only looks for ACAO
// believed the preflight had passed.
func TestPreflightRefusesAHeaderNoRuleAllows(t *testing.T) {
	withCORSTestFixture(t)
	setTestSetting(conf.S3BucketCors,
		`{"bucket":[{"allowed_origins":["https://app.example"],"allowed_methods":["GET"],`+
			`"allowed_headers":["x-amz-*"],"max_age_seconds":600}]}`)

	chain := newCORSTestChain(nil)

	denied := doCORSRequest(t, chain,
		corsPreflight(t, "/bucket/file.txt", "https://app.example", "GET", "content-type"))
	require.Equal(t, http.StatusOK, denied.Code)
	assert.Empty(t, denied.Header().Get("Access-Control-Allow-Origin"), "a refused preflight must not carry an allowed origin")
	assert.Empty(t, denied.Header().Get("Access-Control-Allow-Methods"))
	assert.Empty(t, denied.Header().Get("Access-Control-Allow-Headers"))
	assert.Contains(t, strings.Join(denied.Header().Values("Vary"), " "), "Access-Control-Request-Headers")

	allowed := doCORSRequest(t, chain,
		corsPreflight(t, "/bucket/file.txt", "https://app.example", "GET", "x-amz-date"))
	require.Equal(t, http.StatusOK, allowed.Code)
	assert.Equal(t, "https://app.example", allowed.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "x-amz-date", allowed.Header().Get("Access-Control-Allow-Headers"))
}

// TestPreflightAdvertisesCredentialsAndKeepsConfiguredMaxAge pins the two headers
// a browser needs to run a cross-origin request that carries credentials, and
// that Max-Age is a configured value rather than a hardcoded 0 (which made every
// single request revalidate its preflight).
func TestPreflightAdvertisesCredentialsAndKeepsConfiguredMaxAge(t *testing.T) {
	withCORSTestFixture(t)
	setTestSetting(conf.S3BucketCors,
		`{"bucket":[{"allowed_origins":["https://app.example"],"allowed_methods":["GET"]}]}`)

	recorder := doCORSRequest(t, newCORSTestChain(nil),
		corsPreflight(t, "/bucket/file.txt", "https://app.example", "GET", ""))
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "true", recorder.Header().Get("Access-Control-Allow-Credentials"))
	assert.Empty(t, recorder.Header().Get("Access-Control-Max-Age"),
		"no MaxAgeSeconds was configured, so S3 sends no Max-Age header at all")

	setTestSetting(conf.S3BucketCors,
		`{"bucket":[{"allowed_origins":["https://app.example"],"allowed_methods":["GET"],"max_age_seconds":600}]}`)
	recorder = doCORSRequest(t, newCORSTestChain(nil),
		corsPreflight(t, "/bucket/file.txt", "https://app.example", "GET", ""))
	assert.Equal(t, "600", recorder.Header().Get("Access-Control-Max-Age"))
}

// TestExpiredPresignedURLIsForbidden pins the status of an expired presigned
// link. gofakes3 answers it with 400 (signature-errors.go:127 hardcodes it)
// while the gateway's own ceiling answers 403, so the same refusal reported two
// different statuses and every SDK classified one of them as a client error to
// retry.
func TestExpiredPresignedURLIsForbidden(t *testing.T) {
	guard := presignedLifetimeGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("an expired presigned URL reached the handler")
	}))

	expired := time.Now().UTC().Add(-30 * time.Minute).Format("20060102T150405Z")
	recorder := httptest.NewRecorder()
	guard.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/bucket/obj?X-Amz-Signature=abc&X-Amz-Date="+expired+"&X-Amz-Expires=60", nil))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "AccessDenied")

	// A V2 link spells the expiry as an absolute timestamp.
	past := time.Now().Add(-time.Minute).Unix()
	recorder = httptest.NewRecorder()
	guard.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/bucket/obj?Signature=abc&AWSAccessKeyId=k&Expires="+strconv.FormatInt(past, 10), nil))
	require.Equal(t, http.StatusForbidden, recorder.Code)

	// A link that is still valid keeps working.
	reached := false
	guard = presignedLifetimeGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	fresh := time.Now().UTC().Format("20060102T150405Z")
	guard.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet,
		"/bucket/obj?X-Amz-Signature=abc&X-Amz-Date="+fresh+"&X-Amz-Expires=60", nil))
	assert.True(t, reached, "a link inside its lifetime must still authenticate")
}

// TestMultiRangeIsAnsweredWithTheWholeObject pins that a Range header naming
// more than one range is ignored the way S3 ignores it. gofakes3 answers it with
// 501 NotImplemented (ranges.go:83), which fails a read every client expects to
// succeed.
func TestMultiRangeIsAnsweredWithTheWholeObject(t *testing.T) {
	handler, _, _ := sizeChain(t)
	const payload = "0123456789"
	if rec := openModeRequest(handler, http.MethodPut, "/mp/multi-range.bin",
		strings.NewReader(payload), nil); rec.Code != http.StatusOK {
		t.Fatalf("put: code=%d body=%s", rec.Code, rec.Body.String())
	}

	for _, header := range []string{"bytes=0-0,2-2", "bytes=0-1, 3-4"} {
		rec := openModeRequest(handler, http.MethodGet, "/mp/multi-range.bin", nil,
			map[string]string{"Range": header})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET with Range %q: code=%d body=%s, want 200", header, rec.Code, rec.Body.String())
		}
		if rec.Body.String() != payload {
			t.Fatalf("GET with Range %q returned %q, want the whole object", header, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Range"); got != "" {
			t.Fatalf("GET with Range %q answered Content-Range %q, want none", header, got)
		}
	}

	// A single range still works.
	rec := openModeRequest(handler, http.MethodGet, "/mp/multi-range.bin", nil,
		map[string]string{"Range": "bytes=2-4"})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("single-range GET: code=%d, want 206", rec.Code)
	}
	if rec.Body.String() != "234" {
		t.Fatalf("single-range GET returned %q, want %q", rec.Body.String(), "234")
	}
}

// TestMultiDeleteDigestMismatchIsRefused pins that the Content-MD5 of a
// DeleteObjects body is compared against the bytes that arrived. S3 requires the
// header so that a body damaged in transit is refused instead of deleting the
// wrong keys; gofakes3 read the key list and ignored the digest.
func TestMultiDeleteDigestMismatchIsRefused(t *testing.T) {
	handler, _, _ := sizeChain(t)
	for _, key := range []string{"keep-a.txt", "keep-b.txt"} {
		if rec := openModeRequest(handler, http.MethodPut, "/mp/"+key, strings.NewReader("x"), nil); rec.Code != http.StatusOK {
			t.Fatalf("put %s: code=%d", key, rec.Code)
		}
	}
	document := `<Delete><Object><Key>keep-a.txt</Key></Object></Delete>`

	rec := openModeRequest(handler, http.MethodPost, "/mp?delete", strings.NewReader(document),
		map[string]string{"Content-MD5": digestOf("something else entirely")})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong Content-MD5: code=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if code := s3ErrorCodeIn(t, rec.Body.String()); code != "InvalidDigest" {
		t.Fatalf("wrong Content-MD5: code=%q, want InvalidDigest", code)
	}
	// Nothing was deleted by the refused request.
	if got := objectBody(t, handler, "/mp/keep-a.txt"); got != "x" {
		t.Fatalf("the refused delete removed the object: %q", got)
	}

	rec = openModeRequest(handler, http.MethodPost, "/mp?delete", strings.NewReader(document),
		map[string]string{"Content-MD5": digestOf(document)})
	if rec.Code != http.StatusOK {
		t.Fatalf("correct Content-MD5: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := openModeRequest(handler, http.MethodGet, "/mp/keep-a.txt", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("the accepted delete did not remove the key: code=%d", rec.Code)
	}
	if got := objectBody(t, handler, "/mp/keep-b.txt"); got != "x" {
		t.Fatalf("the delete touched a key that was not listed: %q", got)
	}
}

func digestOf(body string) string {
	sum := md5.Sum([]byte(body))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// trailerFrames frames payload as an aws-chunked body and appends the trailer
// section a streaming upload uses to declare its checksum.
func trailerFrames(payload string, chunkSize int, trailer map[string]string) string {
	var builder strings.Builder
	signature := strings.Repeat("a", 64)
	for rest := payload; len(rest) > 0; {
		size := len(rest)
		if size > chunkSize {
			size = chunkSize
		}
		fmt.Fprintf(&builder, "%x;chunk-signature=%s\r\n%s\r\n", size, signature, rest[:size])
		rest = rest[size:]
	}
	fmt.Fprintf(&builder, "0;chunk-signature=%s\r\n", signature)
	for key, value := range trailer {
		fmt.Fprintf(&builder, "%s:%s\r\n", key, value)
	}
	builder.WriteString("\r\n")
	return builder.String()
}

// trailerHeaders are the request headers of a streaming upload that declares the
// checksum named in trailerName in its trailer section.
func trailerHeaders(decodedLength int, trailerName string) map[string]string {
	return map[string]string{
		"Content-Encoding":             "aws-chunked",
		"X-Amz-Content-Sha256":         "STREAMING-UNSIGNED-PAYLOAD-TRAILER",
		"X-Amz-Decoded-Content-Length": fmt.Sprint(decodedLength),
		"X-Amz-Trailer":                trailerName,
	}
}

func sha256Base64(body string) string {
	sum := sha256.Sum256([]byte(body))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// TestChunkedTrailerChecksumIsVerified pins that the checksum a streaming upload
// declares in its trailer is compared with the body. The trailer is the only
// integrity value such an upload can carry, and the gateway stored a body that
// contradicted it with 200 OK - the value the client asked to be honoured was a
// decoration.
func TestChunkedTrailerChecksumIsVerified(t *testing.T) {
	handler, _, _ := sizeChain(t)
	const payload = "the bytes the client sent, twice over"

	rec := openModeRequest(handler, http.MethodPut, "/mp/trailer.bin",
		strings.NewReader(trailerFrames(payload, 5, map[string]string{
			"x-amz-checksum-sha256": sha256Base64(payload),
		})), trailerHeaders(len(payload), "x-amz-checksum-sha256"))
	require.Equal(t, http.StatusOK, rec.Code, "a body that matches its trailer was refused: %s", rec.Body.String())
	if got := objectBody(t, handler, "/mp/trailer.bin"); got != payload {
		t.Fatalf("stored body = %q, want %q", got, payload)
	}

	// The same request with a checksum that does not describe the body.
	wrong := base64.StdEncoding.EncodeToString([]byte("not the digest of this body!!"))
	rec = openModeRequest(handler, http.MethodPut, "/mp/trailer-wrong.bin",
		strings.NewReader(trailerFrames(payload, 5, map[string]string{
			"x-amz-checksum-sha256": wrong,
		})), trailerHeaders(len(payload), "x-amz-checksum-sha256"))
	require.Equal(t, http.StatusBadRequest, rec.Code,
		"a body that contradicts its trailer answered %d: %s", rec.Code, rec.Body.String())
	if code := s3ErrorCodeIn(t, rec.Body.String()); code != "BadDigest" {
		t.Fatalf("code = %q, want BadDigest", code)
	}
	if got := openModeRequest(handler, http.MethodGet, "/mp/trailer-wrong.bin", nil, nil); got.Code != http.StatusNotFound {
		t.Fatalf("the refused body was published anyway: GET answered %d", got.Code)
	}
}

// TestChunkedTrailerChecksumIsVerifiedOnUploadPart pins the same check on the
// part path, which streams through its own copy loop.
func TestChunkedTrailerChecksumIsVerifiedOnUploadPart(t *testing.T) {
	handler, _, _ := sizeChain(t)
	uploadID := initiateUpload(t, handler, "/mp/trailer-part.bin")
	const payload = "part payload"

	wrong := base64.StdEncoding.EncodeToString([]byte("nope"))
	rec := openModeRequest(handler, http.MethodPut,
		"/mp/trailer-part.bin?partNumber=1&uploadId="+uploadID,
		strings.NewReader(trailerFrames(payload, 4, map[string]string{"x-amz-checksum-sha256": wrong})),
		trailerHeaders(len(payload), "x-amz-checksum-sha256"))
	require.Equal(t, http.StatusBadRequest, rec.Code,
		"a part that contradicts its trailer answered %d: %s", rec.Code, rec.Body.String())

	// The part was not accepted, so a completion cannot publish it.
	completion := openModeRequest(handler, http.MethodPost, "/mp/trailer-part.bin?uploadId="+uploadID,
		strings.NewReader(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"x"</ETag></Part></CompleteMultipartUpload>`), nil)
	if completion.Code == http.StatusOK {
		t.Fatalf("completion of an upload whose only part was refused answered 200: %s", completion.Body.String())
	}
}

// TestChunkedTrailerWithAnUnverifiableAlgorithmIsIgnored pins that a trailer this
// gateway cannot verify is not a reason to refuse a body: rejecting an upload
// whose checksum could not be checked would be worse than accepting it.
func TestChunkedTrailerWithAnUnverifiableAlgorithmIsIgnored(t *testing.T) {
	handler, _, _ := sizeChain(t)
	const payload = "crc64nvme is not offered by this gateway"
	rec := openModeRequest(handler, http.MethodPut, "/mp/trailer-crc64.bin",
		strings.NewReader(trailerFrames(payload, 7, map[string]string{
			"x-amz-checksum-crc64nvme": "AAAAAAAAAAA=",
		})), trailerHeaders(len(payload), "x-amz-checksum-crc64nvme"))
	require.Equal(t, http.StatusOK, rec.Code, "an unverifiable trailer refused the body: %s", rec.Body.String())
	if got := objectBody(t, handler, "/mp/trailer-crc64.bin"); got != payload {
		t.Fatalf("stored body = %q", got)
	}
}
