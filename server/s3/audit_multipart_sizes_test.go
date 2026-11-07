package s3

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

// This file pins the size and length semantics of the multipart surface: the
// per-part ceiling, what a declared-but-lying aws-chunked body does to the
// connection, empty copy sources, and the missing 5 MiB floor at completion.

// sizeChain builds the same chain openModeChain builds, but hands back the
// backend and the local root directory as well: the per-part ceiling can only
// be exercised against a source object bigger than 5 GiB, which a unit test
// creates as a sparse file instead of uploading.
func sizeChain(t *testing.T) (http.Handler, *s3Backend, string) {
	t.Helper()
	backend, localRoot := setupMultipartBackend(t)
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
	return handler, backend, localRoot
}

// sparseFile writes a file of the given size that occupies no disk blocks, so
// a test can present a source object far above the 5 GiB part ceiling.
func sparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatalf("truncate %s to %d: %v", path, size, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if st.Size() != size {
		t.Fatalf("%s is %d bytes, want %d", path, st.Size(), size)
	}
}

// s3ErrorCodeIn returns the <Code> element of an S3 error document.
func s3ErrorCodeIn(t *testing.T, body string) string {
	t.Helper()
	var doc struct {
		Code string `xml:"Code"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("parsing the error document %q: %v", body, err)
	}
	return doc.Code
}

// objectBody reads an object back through the chain.
func objectBody(t *testing.T, handler http.Handler, target string) string {
	t.Helper()
	rec := openModeRequest(handler, http.MethodGet, target, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: code=%d body=%s", target, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// TestUploadPartCopyRefusesPartLargerThanFiveGiB pins that the 5 GiB per-part
// ceiling covers the copy path too.
//
// UploadPartCopy does not stream a client body: it asks the storage for the
// source object and hands the resulting link to UploadPart with the source's
// length. The length therefore comes from the store, not from the request, and
// the ceiling UploadPart applies to a declared content-length (multipart.go:174)
// has to catch it as well - otherwise a single copy request could make the
// gateway write an unbounded part, which is exactly what the ceiling exists to
// prevent.
func TestUploadPartCopyRefusesPartLargerThanFiveGiB(t *testing.T) {
	handler, _, localRoot := sizeChain(t)

	sparseFile(t, filepath.Join(localRoot, "huge.bin"), maxUploadPartSize+4096)

	uploadID := initiateUpload(t, handler, "/mp/huge-copy.bin")

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"whole object", map[string]string{"X-Amz-Copy-Source": "/mp/huge.bin"}},
		{"range over the ceiling", map[string]string{
			"X-Amz-Copy-Source":       "/mp/huge.bin",
			"X-Amz-Copy-Source-Range": fmt.Sprintf("bytes=0-%d", maxUploadPartSize),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := openModeRequest(handler, http.MethodPut,
				fmt.Sprintf("/mp/huge-copy.bin?partNumber=1&uploadId=%s", uploadID), nil, tc.headers)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("copy of a >5 GiB source: code=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			if code := s3ErrorCodeIn(t, rec.Body.String()); code != "EntityTooLarge" {
				t.Fatalf("copy of a >5 GiB source: code=%q, want EntityTooLarge (body=%s)",
					code, rec.Body.String())
			}
		})
	}

	// The refused copies must not have stored anything.
	if doc := listParts(t, handler, "/mp/huge-copy.bin", uploadID); len(doc.Parts) != 0 {
		t.Fatalf("ListParts reports %d parts after the refused copies: %+v", len(doc.Parts), doc.Parts)
	}
}

// TestUploadPartSizeGateIsExact pins the boundary of the per-part ceiling: a
// declared length of exactly 5 GiB has to pass the gate (the bytes are never
// read: the body fails on its first Read), one byte more has to be refused.
func TestUploadPartSizeGateIsExact(t *testing.T) {
	_, backend, _ := sizeChain(t)
	ctx := context.Background()

	uploadID, err := backend.CreateMultipartUpload(ctx, "mp", "boundary.bin",
		map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}

	// Past the gate the body is streamed: an erroring reader proves the size
	// check let the part through without writing 5 GiB.
	_, err = backend.UploadPart(ctx, "mp", "boundary.bin", uploadID, 1, maxUploadPartSize,
		io.MultiReader(strings.NewReader("x"), errReader{}))
	if err == nil {
		t.Fatal("a part whose body fails is reported as stored")
	}
	if code := strings.ToUpper(err.Error()); strings.Contains(code, "ENTITYTOOLARGE") {
		t.Fatalf("exactly 5 GiB was refused: %v", err)
	}

	_, err = backend.UploadPart(ctx, "mp", "boundary.bin", uploadID, 1, maxUploadPartSize+1,
		errReader{})
	if err == nil {
		t.Fatal("a 5 GiB+1 part was accepted")
	}
	if code := s3ErrorCode(err); code != "EntityTooLarge" {
		t.Fatalf("a 5 GiB+1 part: code=%q (%v), want EntityTooLarge", code, err)
	}
}

// errReader fails on the first Read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("body refused by the test") }

// chunkedFrames encodes payload as aws-chunked frames of the given chunk size,
// with chunk signatures that are syntactically valid and semantically bogus.
func chunkedFrames(payload string, chunkSize int) string {
	signature := strings.Repeat("a", 64)
	var builder strings.Builder
	for len(payload) > 0 {
		size := len(payload)
		if size > chunkSize {
			size = chunkSize
		}
		fmt.Fprintf(&builder, "%x;chunk-signature=%s\r\n%s\r\n", size, signature, payload[:size])
		payload = payload[size:]
	}
	fmt.Fprintf(&builder, "0;chunk-signature=%s\r\n\r\n", signature)
	return builder.String()
}

// rawRequest writes one HTTP/1.1 request onto conn, Content-Length included.
func rawRequest(t *testing.T, conn net.Conn, method, target string, headers map[string]string, body string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\nHost: gateway.test\r\n", method, target)
	for key, value := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", key, value)
	}
	if method == http.MethodPut || method == http.MethodPost {
		fmt.Fprintf(&b, "Content-Length: %d\r\n", len(body))
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()+body); err != nil {
		t.Fatalf("writing %s %s: %v", method, target, err)
	}
}

// rawResponse reads and drains one response.
func rawResponse(t *testing.T, br *bufio.Reader, method string) (*http.Response, string) {
	t.Helper()
	resp, err := http.ReadResponse(br, &http.Request{Method: method})
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing the response body: %v", err)
	}
	return resp, string(body)
}

// streamingHeaders is what an SDK sends for a chunked (sigv4 streaming) upload.
func streamingHeaders(decodedLength int) map[string]string {
	return map[string]string{
		"Content-Encoding":             "aws-chunked",
		"X-Amz-Content-Sha256":         "STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
		"X-Amz-Decoded-Content-Length": fmt.Sprint(decodedLength),
	}
}

// TestChunkedOverDeliveryIsRefused pins that a body which frames more bytes than
// it declared in x-amz-decoded-content-length is refused instead of being cut
// off and stored.
//
// gofakes3 caps the decoded body at the declared length (gofakes3.go:775
// `reader = io.LimitReader(reader, size)`), so everything downstream of it -
// including putStream's own length check - only ever sees the announced prefix:
// the surplus disappeared silently and the truncated object was published with
// 200 OK, with an ETag computed from what actually arrived, so no client-side
// checksum could notice either. S3 refuses the request; so does the gateway now,
// and the surplus stays unread body of *this* request, so net/http drains it
// (server.go:1478-1481, up to maxPostHandlerReadBytes = 256 KiB) or drops the
// connection instead of re-parsing it as a request line.
func TestChunkedOverDeliveryIsRefused(t *testing.T) {
	handler, _, _ := sizeChain(t)

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	reader := bufio.NewReader(conn)

	const declared, framed = 4, "abcdefghij"
	rawRequest(t, conn, http.MethodPut, "/mp/over-delivery.bin", streamingHeaders(declared),
		chunkedFrames(framed, 2))

	resp, body := rawResponse(t, reader, http.MethodPut)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("over-delivering PUT: code=%d body=%s, want 400", resp.StatusCode, body)
	}
	if code := s3ErrorCodeIn(t, body); code != "IncompleteBody" {
		t.Fatalf("over-delivering PUT: code=%q, want IncompleteBody", code)
	}
	if resp.Close {
		t.Fatal("a body of ~200 surplus bytes made the server drop the connection; " +
			"the surplus should have been drained instead")
	}

	// Nothing was published: a truncated object is worse than a failed upload.
	rawRequest(t, conn, http.MethodGet, "/mp/over-delivery.bin", nil, "")
	second, secondBody := rawResponse(t, reader, http.MethodGet)
	if second.StatusCode != http.StatusNotFound {
		t.Fatalf("the refused key is readable: code=%d body=%s", second.StatusCode, secondBody)
	}
	if code := s3ErrorCodeIn(t, secondBody); code != "NoSuchKey" {
		t.Fatalf("the refused key answered code=%q, want NoSuchKey", code)
	}
}

// TestChunkedOverDeliveryBeyondDrainLimitClosesConnection pins the other half of
// net/http's policy: a surplus too large to drain is answered with
// "Connection: close" instead of being left in the connection.
func TestChunkedOverDeliveryBeyondDrainLimitClosesConnection(t *testing.T) {
	handler, _, _ := sizeChain(t)

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	reader := bufio.NewReader(conn)

	// 400 KiB of surplus: more than maxPostHandlerReadBytes (256 KiB).
	payload := strings.Repeat("z", 400<<10)
	rawRequest(t, conn, http.MethodPut, "/mp/over-big.bin", streamingHeaders(4),
		chunkedFrames(payload, 64<<10))

	resp, body := rawResponse(t, reader, http.MethodPut)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("over-delivering PUT: code=%d body=%s, want 400", resp.StatusCode, body)
	}
	if !resp.Close {
		// net/http promotes "Connection: close" into Response.Close and drops
		// the header, so the field is what a client sees.
		t.Fatalf("a 400 KiB surplus answered with a kept-alive connection (Connection: %q)",
			resp.Header.Get("Connection"))
	}
	// The connection must be gone rather than reused with 400 KiB of the
	// previous body still queued in it.
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("the connection stayed open with 400 KiB of the previous body unread")
	}
	if rec := openModeRequest(handler, http.MethodGet, "/mp/over-big.bin", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("the refused key answered %d, want 404", rec.Code)
	}
}

// TestChunkedPartOverDeliveryIsTruncatedToDeclaredLength pins the same behaviour
// on the part path, which reaches UploadPart with the decoded length gofakes3
// parsed (gofakes3.go:1058-1078) and a LimitReader over the decoded frames.
func TestChunkedPartOverDeliveryIsRefused(t *testing.T) {
	handler, _, _ := sizeChain(t)
	uploadID := initiateUpload(t, handler, "/mp/over-part.bin")

	const declared, framed = 4, "abcdefghij"
	rec := openModeRequest(handler, http.MethodPut,
		"/mp/over-part.bin?partNumber=1&uploadId="+uploadID, strings.NewReader(chunkedFrames(framed, 2)),
		streamingHeaders(declared))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-delivering part upload: code=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	// The part is not stored at all, so a completion cannot publish a body that
	// the client never fully delivered.
	if doc := listParts(t, handler, "/mp/over-part.bin", uploadID); len(doc.Parts) != 0 {
		t.Fatalf("ListParts reports %+v, want no parts", doc.Parts)
	}
}

// completePart is one entry of a CompleteMultipartUpload document.
type completePart struct {
	PartNumber int
	ETag       string
}

// completeUpload completes an upload and requires the success answer.
func completeUpload(t *testing.T, handler http.Handler, target, uploadID string, parts []completePart) string {
	t.Helper()
	var body strings.Builder
	body.WriteString("<CompleteMultipartUpload>")
	for _, p := range parts {
		fmt.Fprintf(&body, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", p.PartNumber, p.ETag)
	}
	body.WriteString("</CompleteMultipartUpload>")

	rec := openModeRequest(handler, http.MethodPost, target+"?uploadId="+uploadID,
		strings.NewReader(body.String()), map[string]string{"Content-Type": "application/xml"})
	if rec.Code != http.StatusOK {
		t.Fatalf("Complete %s: code=%d body=%s", target, rec.Code, rec.Body.String())
	}
	var doc struct {
		ETag string `xml:"ETag"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the complete response %q: %v", rec.Body.String(), err)
	}
	return doc.ETag
}

// TestChunkedSameLengthSubstitutionIsAccepted documents the third case: a body
// that declares N bytes and sends N *different* bytes is stored and answered
// with 200. Nothing in the chain can notice it - the declared payload hash for a
// streaming payload is the literal string STREAMING-AWS4-HMAC-SHA256-PAYLOAD,
// not a digest, and gofakes3's chunked reader parses chunk-signature without ever
// verifying it (chunk.go:23-70).
func TestChunkedSameLengthSubstitutionIsAccepted(t *testing.T) {
	handler, _, _ := sizeChain(t)

	const payload = "XXXXXXXX"
	rec := openModeRequest(handler, http.MethodPut, "/mp/substituted.bin",
		strings.NewReader(chunkedFrames(payload, 4)), streamingHeaders(len(payload)))
	if rec.Code != http.StatusOK {
		t.Fatalf("substituted streaming PUT: code=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if got := objectBody(t, handler, "/mp/substituted.bin"); got != payload {
		t.Fatalf("stored object is %q, want %q", got, payload)
	}
}

// TestUploadPartCopyOfEmptySourceProducesEmptyPart pins what copying a zero-byte
// object yields: a zero-byte part, accepted by Complete, assembling into an
// empty object.
func TestUploadPartCopyOfEmptySourceProducesEmptyPart(t *testing.T) {
	handler, _, _ := sizeChain(t)

	if rec := openModeRequest(handler, http.MethodPut, "/mp/empty.bin",
		strings.NewReader(""), nil); rec.Code != http.StatusOK {
		t.Fatalf("seeding the empty source: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := objectBody(t, handler, "/mp/empty.bin"); got != "" {
		t.Fatalf("the empty source holds %q", got)
	}

	uploadID := initiateUpload(t, handler, "/mp/empty-copy.bin")
	rec := openModeRequest(handler, http.MethodPut,
		"/mp/empty-copy.bin?partNumber=1&uploadId="+uploadID, nil,
		map[string]string{"X-Amz-Copy-Source": "/mp/empty.bin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("copying an empty source: code=%d body=%s", rec.Code, rec.Body.String())
	}
	etag := copyPartETag(t, rec.Body.String())
	if want := `"d41d8cd98f00b204e9800998ecf8427e"`; etag != want {
		t.Fatalf("the empty part's ETag is %s, want %s", etag, want)
	}

	doc := listParts(t, handler, "/mp/empty-copy.bin", uploadID)
	if len(doc.Parts) != 1 || doc.Parts[0].Size != 0 {
		t.Fatalf("ListParts reports %+v, want a single 0-byte part", doc.Parts)
	}

	completeUpload(t, handler, "/mp/empty-copy.bin", uploadID,
		[]completePart{{PartNumber: 1, ETag: etag}})
	if got := objectBody(t, handler, "/mp/empty-copy.bin"); got != "" {
		t.Fatalf("the assembled object holds %q, want nothing", got)
	}
}

// TestUploadPartCopyOfEmptySourceWithRangeIsInvalidRange pins the range form: a
// slice of an empty object does not exist, and saying so beats answering 200
// with an empty part.
func TestUploadPartCopyOfEmptySourceWithRangeIsInvalidRange(t *testing.T) {
	handler, _, _ := sizeChain(t)

	if rec := openModeRequest(handler, http.MethodPut, "/mp/empty2.bin",
		strings.NewReader(""), nil); rec.Code != http.StatusOK {
		t.Fatalf("seeding the empty source: code=%d body=%s", rec.Code, rec.Body.String())
	}

	uploadID := initiateUpload(t, handler, "/mp/empty-range.bin")
	rec := openModeRequest(handler, http.MethodPut,
		"/mp/empty-range.bin?partNumber=1&uploadId="+uploadID, nil,
		map[string]string{
			"X-Amz-Copy-Source":       "/mp/empty2.bin",
			"X-Amz-Copy-Source-Range": "bytes=0-0",
		})
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("range copy of an empty source: code=%d body=%s, want 416", rec.Code, rec.Body.String())
	}
	if code := s3ErrorCodeIn(t, rec.Body.String()); code != "InvalidRange" {
		t.Fatalf("range copy of an empty source: code=%q, want InvalidRange", code)
	}
}

// TestCompleteAcceptsPartsSmallerThanFiveMiB documents the deliberate missing
// floor: S3 answers EntityTooSmall for any part but the last below 5 MiB, this
// gateway accepts it and assembles the object (multipart.go:569-575).
func TestCompleteAcceptsPartsSmallerThanFiveMiB(t *testing.T) {
	handler, _, _ := sizeChain(t)
	uploadID := initiateUpload(t, handler, "/mp/small-parts.bin")

	parts := []completePart{}
	for i, body := range []string{"a", "b"} {
		rec := openModeRequest(handler, http.MethodPut,
			fmt.Sprintf("/mp/small-parts.bin?partNumber=%d&uploadId=%s", i+1, uploadID),
			strings.NewReader(body), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("uploading part %d: code=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
		doc := listParts(t, handler, "/mp/small-parts.bin", uploadID)
		var etag string
		for _, p := range doc.Parts {
			if p.PartNumber == i+1 {
				etag = p.ETag
			}
		}
		if etag == "" {
			t.Fatalf("part %d is missing from ListParts: %+v", i+1, doc.Parts)
		}
		parts = append(parts, completePart{PartNumber: i + 1, ETag: etag})
	}

	etag := completeUpload(t, handler, "/mp/small-parts.bin", uploadID, parts)
	// zerofs variant: the ETag is the plain MD5 of the assembled content, not the
	// composite "md5(md5(part)...)-N" (a composite value would have to be stored
	// as a side row, and this variant keeps stored objects free of
	// gateway-generated metadata).
	sum := md5.Sum([]byte("ab"))
	if got := strings.Trim(etag, `"`); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("ETag is %s, want the MD5 of the assembled content", etag)
	}
	if got := objectBody(t, handler, "/mp/small-parts.bin"); got != "ab" {
		t.Fatalf("assembled object is %q, want %q", got, "ab")
	}
}

// TestBrokenChunkFrameIsAnsweredAsAClientError pins that a request body whose
// aws-chunked framing does not decode is refused as the client error it is, on
// both write paths, and that neither path leaves anything behind.
//
// gofakes3 decodes the frames itself (gofakes3/chunk.go:23) and a frame header
// that is not a hexadecimal size comes back to the caller as whatever
// fmt.Fscanf produced: a plain error with no S3 code on it. gofakes3 renders
// every unclassified error as 500 InternalError, so a client whose framing is
// broken was told the gateway had failed - the one answer every SDK retries,
// with the same body, forever - and the gateway logged it as its own fault.
// The body is the only source of that error, so it belongs in the request's
// column: 400, the code S3 uses for a body it cannot read.
func TestBrokenChunkFrameIsAnsweredAsAClientError(t *testing.T) {
	handler, _, _ := sizeChain(t)
	// "zz" is not a hexadecimal chunk size, so the frame cannot be decoded at
	// all; the payload behind it never matters.
	broken := "zz;chunk-signature=" + strings.Repeat("a", 64) + "\r\nabcd\r\n"

	t.Run("whole object", func(t *testing.T) {
		rec := openModeRequest(handler, http.MethodPut, "/mp/broken-frame.bin",
			strings.NewReader(broken), streamingHeaders(4))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT with an undecodable frame: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if code := s3ErrorCodeIn(t, rec.Body.String()); code != "IncompleteBody" {
			t.Fatalf("error code = %q, want IncompleteBody", code)
		}
		// A refused PUT publishes nothing: no object, not even a zero-byte one.
		got := openModeRequest(handler, http.MethodGet, "/mp/broken-frame.bin", nil, nil)
		if got.Code != http.StatusNotFound {
			t.Fatalf("GET after a refused PUT: code=%d body=%s", got.Code, got.Body.String())
		}
	})

	t.Run("part", func(t *testing.T) {
		target := "/mp/broken-frame-part.bin"
		uploadID := initiateUpload(t, handler, target)
		rec := openModeRequest(handler, http.MethodPut,
			target+"?partNumber=1&uploadId="+uploadID, strings.NewReader(broken), streamingHeaders(4))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("UploadPart with an undecodable frame: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if code := s3ErrorCodeIn(t, rec.Body.String()); code != "IncompleteBody" {
			t.Fatalf("error code = %q, want IncompleteBody", code)
		}
		// The refused part must not be recorded either: otherwise a Complete
		// would assemble bytes the client was told were not stored.
		if doc := listParts(t, handler, target, uploadID); len(doc.Parts) != 0 {
			t.Fatalf("ListParts after a refused part = %d parts, want 0", len(doc.Parts))
		}
	})
}

// TestUploadPartCopyOfASliceFromAHugeSourceIsAllowed is the other side of the
// ceiling: it has to refuse the oversized copy without refusing the legitimate
// one. Copying a small slice *out of* an object larger than 5 GiB is the normal
// way to move a big object through multipart, so it must still work.
func TestUploadPartCopyOfASliceFromAHugeSourceIsAllowed(t *testing.T) {
	handler, _, localRoot := sizeChain(t)

	sparseFile(t, filepath.Join(localRoot, "huge2.bin"), maxUploadPartSize+4096)

	uploadID := initiateUpload(t, handler, "/mp/huge-slice.bin")
	const slice = 1 << 20
	rec := openModeRequest(handler, http.MethodPut,
		"/mp/huge-slice.bin?partNumber=1&uploadId="+uploadID, nil,
		map[string]string{
			"X-Amz-Copy-Source":       "/mp/huge2.bin",
			"X-Amz-Copy-Source-Range": fmt.Sprintf("bytes=0-%d", slice-1),
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("slice copy out of a >5 GiB source: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if etag := copyPartETag(t, rec.Body.String()); etag == "" {
		t.Fatal("the slice copy carries no ETag")
	}

	doc := listParts(t, handler, "/mp/huge-slice.bin", uploadID)
	if len(doc.Parts) != 1 || doc.Parts[0].Size != slice {
		t.Fatalf("ListParts reports %+v, want one part of %d bytes", doc.Parts, slice)
	}
}
