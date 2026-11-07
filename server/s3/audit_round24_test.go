package s3

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestListPartsRefusesOutOfRangePaging pins the answer for paging values outside
// the range S3 documents.
//
// max-parts=0 used to select no part while still flagging the page as truncated,
// and the code then indexed the last part of an empty slice: the request answered
// 500 InternalError, the one status every SDK retries, so a client that asked for
// an empty page resent a request that could never succeed. An out-of-range marker
// was passed on to gofakes3 instead of being refused, and max-parts=-1 was
// silently read as "no limit".
func TestListPartsRefusesOutOfRangePaging(t *testing.T) {
	handler, _, _ := sizeChain(t)
	uploadID := initiateUpload(t, handler, "/mp/parts-paging.bin")
	for _, number := range []int{1, 2} {
		rec := openModeRequest(handler, http.MethodPut,
			"/mp/parts-paging.bin?partNumber="+itoaSmall(number)+"&uploadId="+uploadID,
			strings.NewReader("part"), nil)
		require.Equal(t, http.StatusOK, rec.Code, "part %d: %s", number, rec.Body.String())
	}

	for _, query := range []string{
		"max-parts=0", "max-parts=-1", "max-parts=1001", "max-parts=abc",
		"part-number-marker=-1",
	} {
		rec := openModeRequest(handler, http.MethodGet,
			"/mp/parts-paging.bin?uploadId="+uploadID+"&"+query, nil, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code,
			"%s answered %d, want 400: %s", query, rec.Code, rec.Body.String())
		if code := s3ErrorCodeIn(t, rec.Body.String()); code != "InvalidArgument" {
			t.Fatalf("%s: code = %q, want InvalidArgument", query, code)
		}
	}

	// The boundary itself still works, and the paging cursor points at the last
	// part of the page that was actually listed.
	rec := openModeRequest(handler, http.MethodGet,
		"/mp/parts-paging.bin?uploadId="+uploadID+"&max-parts=1", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.Contains(t, body, "<IsTruncated>true</IsTruncated>")
	require.Contains(t, body, "<NextPartNumberMarker>1</NextPartNumberMarker>")

	// A page past the end is empty and not truncated, which is what used to
	// panic when max-parts selected nothing.
	rec = openModeRequest(handler, http.MethodGet,
		"/mp/parts-paging.bin?uploadId="+uploadID+"&part-number-marker=2&max-parts=5", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "<IsTruncated>false</IsTruncated>")
	rec = openModeRequest(handler, http.MethodGet,
		"/mp/parts-paging.bin?uploadId="+uploadID+"&part-number-marker=10000&max-parts=1", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestCopyObjectRefusesAnUnknownMetadataDirective pins that a directive S3 does
// not define is refused instead of being read as COPY.
func TestCopyObjectRefusesAnUnknownMetadataDirective(t *testing.T) {
	handler := copyAuditChain(t)
	require.Equal(t, http.StatusOK,
		signedPut(t, handler, "/src-bucket/directive.txt", "body", nil, auditRound22Base).Code)

	copySigned := append(append([]string{}, auditRound22Base...), "x-amz-copy-source", "x-amz-metadata-directive")
	rec := signedPut(t, handler, "/dst-bucket/directive.txt", "", map[string]string{
		"X-Amz-Copy-Source":        "/src-bucket/directive.txt",
		"X-Amz-Metadata-Directive": "COPYISH",
	}, copySigned)
	require.Equal(t, http.StatusBadRequest, rec.Code,
		"an unknown metadata directive was accepted: %d %s", rec.Code, rec.Body.String())
	require.Equal(t, "InvalidArgument", s3ErrorCodeIn(t, rec.Body.String()))

	// The two values S3 defines keep working.
	for _, directive := range []string{"COPY", "REPLACE"} {
		rec = signedPut(t, handler, "/dst-bucket/directive-"+strings.ToLower(directive)+".txt", "", map[string]string{
			"X-Amz-Copy-Source":        "/src-bucket/directive.txt",
			"X-Amz-Metadata-Directive": directive,
		}, copySigned)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", directive, rec.Body.String())
	}
}

// itoaSmall spells a small positive integer without pulling strconv into the
// import list for one call.
func itoaSmall(value int) string {
	if value == 0 {
		return "0"
	}
	out := ""
	for value > 0 {
		out = string(rune('0'+value%10)) + out
		value /= 10
	}
	return out
}
