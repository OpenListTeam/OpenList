package s3

import (
	"encoding/base64"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// signedDo signs and sends a request through the audit chain.
func signedDo(t *testing.T, handler http.Handler, req *http.Request, signedNames []string) *httptest.ResponseRecorder {
	t.Helper()
	signV4Header(t, req, "leaky-key", "leaky-secret", signedNames)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

var auditRound22Base = []string{"host", "x-amz-content-sha256", "x-amz-date"}

// TestPutObjectEchoesTheDeclaredChecksum pins that an accepted PutObject repeats
// the whole-object checksum the client declared. The value is stored and served
// on GET/HEAD, but the PUT answer carried no header, so a client with checksum
// mode on (boto3 >= 1.36, AWS CLI v2 by default) had nothing to validate and
// reported a checksum error instead of success.
func TestPutObjectEchoesTheDeclaredChecksum(t *testing.T) {
	handler := copyAuditChain(t)

	body := "checksum echo payload"
	sum := crc32.ChecksumIEEE([]byte(body))
	declared := base64.StdEncoding.EncodeToString([]byte{
		byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum),
	})

	rec := signedPut(t, handler, "/src-bucket/echo.txt", body, map[string]string{
		"X-Amz-Checksum-Crc32": declared,
	}, auditRound22Base)
	if rec.Code != http.StatusOK {
		t.Fatalf("seeding with a checksum failed: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("x-amz-checksum-crc32"); got != declared {
		t.Fatalf("PUT did not echo the checksum: got %q, want %q", got, declared)
	}
}

// TestChecksumsAreOnlyReturnedWhenAskedFor pins the other half of the S3 rule:
// GET and HEAD return x-amz-checksum-* only when the request carries
// `x-amz-checksum-mode: ENABLED`. Returning it unconditionally made botocore
// treat the value as always present and validate bodies that are not the whole
// object.
func TestChecksumsAreOnlyReturnedWhenAskedFor(t *testing.T) {
	handler := copyAuditChain(t)

	body := "checksum gating payload"
	sum := crc32.ChecksumIEEE([]byte(body))
	declared := base64.StdEncoding.EncodeToString([]byte{
		byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum),
	})
	if rec := signedPut(t, handler, "/src-bucket/gated.txt", body, map[string]string{
		"X-Amz-Checksum-Crc32": declared,
	}, auditRound22Base); rec.Code != http.StatusOK {
		t.Fatalf("seeding failed: %d %s", rec.Code, rec.Body.String())
	}

	plain := signedDo(t, handler, httptest.NewRequest(http.MethodGet, "/src-bucket/gated.txt", nil), auditRound22Base)
	if plain.Code != http.StatusOK {
		t.Fatalf("plain GET answered %d: %s", plain.Code, plain.Body.String())
	}
	if got := plain.Header().Get("x-amz-checksum-crc32"); got != "" {
		t.Fatalf("a GET without checksum mode returned the checksum %q", got)
	}

	asked := httptest.NewRequest(http.MethodGet, "/src-bucket/gated.txt", nil)
	asked.Header.Set("X-Amz-Checksum-Mode", "ENABLED")
	rec := signedDo(t, handler, asked, append(append([]string{}, auditRound22Base...), "x-amz-checksum-mode"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET with checksum mode answered %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("x-amz-checksum-crc32"); got != declared {
		t.Fatalf("a GET with checksum mode returned %q, want %q", got, declared)
	}
}

// TestCopyObjectRefusesACopySourceRange pins that x-amz-copy-source-range is
// refused on CopyObject. Only UploadPartCopy may slice: the header went ignored
// on the plain copy path, so a client that asked for 1 KiB of a 1 GiB object
// silently received a full copy as a new object.
func TestCopyObjectRefusesACopySourceRange(t *testing.T) {
	handler := copyAuditChain(t)

	if rec := signedPut(t, handler, "/src-bucket/slice.txt", "0123456789", nil, auditRound22Base); rec.Code != http.StatusOK {
		t.Fatalf("seeding failed: %d %s", rec.Code, rec.Body.String())
	}

	copySigned := append(append([]string{}, auditRound22Base...), "x-amz-copy-source", "x-amz-copy-source-range")
	rec := signedPut(t, handler, "/dst-bucket/sliced.txt", "", map[string]string{
		"X-Amz-Copy-Source":       "/src-bucket/slice.txt",
		"X-Amz-Copy-Source-Range": "bytes=0-3",
	}, copySigned)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a ranged CopyObject was accepted: %d %s", rec.Code, rec.Body.String())
	}
	if got := s3ErrorCodeIn(t, rec.Body.String()); got != "InvalidRequest" {
		t.Fatalf("ranged CopyObject answered %q, want InvalidRequest", got)
	}

	// The plain copy still works, so the guard is not refusing every copy.
	if rec := signedPut(t, handler, "/dst-bucket/copied.txt", "", map[string]string{
		"X-Amz-Copy-Source": "/src-bucket/slice.txt",
	}, append(append([]string{}, auditRound22Base...), "x-amz-copy-source")); rec.Code != http.StatusOK {
		t.Fatalf("a plain CopyObject was refused: %d %s", rec.Code, rec.Body.String())
	}
}

// TestEmptyListV2ReportsKeyCount pins that a ListObjectsV2 answer always carries
// KeyCount. gofakes3 only filled it for non-empty pages, so "prefix matched
// nothing" and "MaxKeys=0" came back without the element.
func TestEmptyListV2ReportsKeyCount(t *testing.T) {
	handler := copyAuditChain(t)

	if rec := signedPut(t, handler, "/src-bucket/present.txt", "x", nil, auditRound22Base); rec.Code != http.StatusOK {
		t.Fatalf("seeding failed: %d %s", rec.Code, rec.Body.String())
	}

	rec := signedDo(t, handler, httptest.NewRequest(http.MethodGet, "/src-bucket?list-type=2&prefix=no-such-prefix", nil), auditRound22Base)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty listing answered %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "<KeyCount>0</KeyCount>") {
		t.Fatalf("empty listing has no KeyCount: %s", rec.Body.String())
	}

	full := signedDo(t, handler, httptest.NewRequest(http.MethodGet, "/src-bucket?list-type=2&prefix=present", nil), auditRound22Base)
	if !strings.Contains(full.Body.String(), "<KeyCount>1</KeyCount>") {
		t.Fatalf("non-empty listing reported a wrong KeyCount: %s", full.Body.String())
	}
}

// TestListURLEncodingCoversStartAfter pins that encoding-type=url encodes
// StartAfter like every other echoed value.
func TestListURLEncodingCoversStartAfter(t *testing.T) {
	handler := copyAuditChain(t)

	for _, target := range []string{"/src-bucket/a%20b.txt", "/src-bucket/z.txt"} {
		if rec := signedPut(t, handler, target, "x", nil, auditRound22Base); rec.Code != http.StatusOK {
			t.Fatalf("seeding %s failed: %d %s", target, rec.Code, rec.Body.String())
		}
	}

	rec := signedDo(t, handler, httptest.NewRequest(http.MethodGet,
		"/src-bucket?list-type=2&encoding-type=url&start-after=a%20b.txt", nil), auditRound22Base)
	if rec.Code != http.StatusOK {
		t.Fatalf("encoded listing answered %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<StartAfter>a b.txt</StartAfter>") {
		t.Fatalf("StartAfter was echoed unencoded: %s", body)
	}
	if !strings.Contains(body, "<StartAfter>a%20b.txt</StartAfter>") {
		t.Fatalf("StartAfter is missing or mis-encoded: %s", body)
	}
}

// TestCompleteMultipartUploadReportsALocation pins that a finished multipart
// upload tells the client where the object is. gofakes3 answered with an empty
// Location, leaving SDKs without a URL for the object they just created.
func TestCompleteMultipartUploadReportsALocation(t *testing.T) {
	handler := copyAuditChain(t)

	create := signedDo(t, handler, httptest.NewRequest(http.MethodPost, "/src-bucket/multi.bin?uploads", nil), auditRound22Base)
	if create.Code != http.StatusOK {
		t.Fatalf("creating the upload failed: %d %s", create.Code, create.Body.String())
	}
	match := regexp.MustCompile(`<UploadId>([^<]+)</UploadId>`).FindStringSubmatch(create.Body.String())
	if match == nil {
		t.Fatalf("no upload id in %s", create.Body.String())
	}
	uploadID := match[1]

	part := signedPut(t, handler, "/src-bucket/multi.bin?partNumber=1&uploadId="+uploadID,
		"0123456789", nil, auditRound22Base)
	if part.Code != http.StatusOK {
		t.Fatalf("uploading the part failed: %d %s", part.Code, part.Body.String())
	}
	etag := part.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("part upload returned no ETag: %s", part.Body.String())
	}

	completeBody := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, strings.Trim(etag, `"`))
	req := httptest.NewRequest(http.MethodPost, "/src-bucket/multi.bin?uploadId="+uploadID, strings.NewReader(completeBody))
	req.Header.Set("Content-Length", fmt.Sprint(len(completeBody)))
	rec := signedDo(t, handler, req, auditRound22Base)
	if rec.Code != http.StatusOK {
		t.Fatalf("completing the upload failed: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<Location></Location>") {
		t.Fatalf("CompleteMultipartUpload returned an empty Location: %s", body)
	}
	if !strings.Contains(body, "<Location>http://") {
		t.Fatalf("CompleteMultipartUpload returned no URL: %s", body)
	}
}

// TestPartNumberOutsideTheAllowedRangeIsAnInvalidArgument pins the error code
// S3 uses for a part number out of 1..10000. gofakes3 answered InvalidPart,
// which clients read as "that part is missing" and answered by re-uploading.
func TestPartNumberOutsideTheAllowedRangeIsAnInvalidArgument(t *testing.T) {
	handler := copyAuditChain(t)

	create := signedDo(t, handler, httptest.NewRequest(http.MethodPost, "/src-bucket/parts.bin?uploads", nil), auditRound22Base)
	match := regexp.MustCompile(`<UploadId>([^<]+)</UploadId>`).FindStringSubmatch(create.Body.String())
	if match == nil {
		t.Fatalf("no upload id in %s", create.Body.String())
	}
	uploadID := match[1]

	for _, number := range []string{"0", "-1", "10001"} {
		rec := signedPut(t, handler, "/src-bucket/parts.bin?partNumber="+number+"&uploadId="+uploadID,
			"x", nil, auditRound22Base)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("partNumber=%s answered %d: %s", number, rec.Code, rec.Body.String())
		}
		if got := s3ErrorCodeIn(t, rec.Body.String()); got != "InvalidArgument" {
			t.Fatalf("partNumber=%s answered %q, want InvalidArgument", number, got)
		}
	}

	// A legal part number still works.
	if rec := signedPut(t, handler, "/src-bucket/parts.bin?partNumber=1&uploadId="+uploadID,
		"x", nil, auditRound22Base); rec.Code != http.StatusOK {
		t.Fatalf("partNumber=1 was refused: %d %s", rec.Code, rec.Body.String())
	}
}
