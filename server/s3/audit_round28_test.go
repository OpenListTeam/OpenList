package s3

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMultipartQueryWithoutAnUploadIsRefused pins that a request carrying
// multipart parameters which cannot name an upload is refused instead of being
// handled as an ordinary object request.
//
// gofakes3 routes on the presence of "?partNumber"/"?uploadId", so an empty
// upload id fell through to the plain handler: "PUT ?partNumber=2" overwrote the
// whole object with that one part, and "DELETE ?uploadId=" deleted the object as
// if it had aborted an upload. Both are silent data loss, and S3 answers 400
// InvalidArgument for each.
func TestMultipartQueryWithoutAnUploadIsRefused(t *testing.T) {
	handler, _, _ := sizeChain(t)

	const original = "ORIGINAL-CONTENT"
	rec := openModeRequest(handler, http.MethodPut, "/mp/lost-upload.bin", strings.NewReader(original), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	refused := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodPut, "/mp/lost-upload.bin?partNumber=2", "PART-BYTES"},
		{http.MethodPut, "/mp/lost-upload.bin?partNumber=1&uploadId=", "PART-BYTES"},
		{http.MethodPut, "/mp/lost-upload.bin?uploadId=", "WHOLE-BYTES"},
		{http.MethodDelete, "/mp/lost-upload.bin?uploadId=", ""},
		{http.MethodDelete, "/mp/lost-upload.bin?uploadId", ""},
	}
	for _, c := range refused {
		var body *strings.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		} else {
			body = strings.NewReader("")
		}
		rec := openModeRequest(handler, c.method, c.target, body, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code,
			"%s %s answered %d, want 400: %s", c.method, c.target, rec.Code, rec.Body.String())
		if code := s3ErrorCodeIn(t, rec.Body.String()); code != "InvalidArgument" {
			t.Fatalf("%s %s: code = %q, want InvalidArgument", c.method, c.target, code)
		}

		// The object must still be exactly what it was before the refused call.
		get := openModeRequest(handler, http.MethodGet, "/mp/lost-upload.bin", nil, nil)
		require.Equal(t, http.StatusOK, get.Code,
			"object disappeared after %s %s: %s", c.method, c.target, get.Body.String())
		require.Equal(t, original, get.Body.String(),
			"%s %s changed the object to %q", c.method, c.target, get.Body.String())
	}

	// A real upload keeps working: upload a part, then abort it.
	uploadID := initiateUpload(t, handler, "/mp/lost-upload.bin")
	rec = openModeRequest(handler, http.MethodPut,
		"/mp/lost-upload.bin?partNumber=1&uploadId="+uploadID, strings.NewReader("part"), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = openModeRequest(handler, http.MethodDelete, "/mp/lost-upload.bin?uploadId="+uploadID, nil, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	get := openModeRequest(handler, http.MethodGet, "/mp/lost-upload.bin", nil, nil)
	require.Equal(t, original, get.Body.String(), "aborting an upload must not touch the object")
}

// TestObjectRequestWithAPartNumberIsRefusedForReadsToo pins the read side of the
// same rule: "?partNumber" without an upload id is a malformed request, not a
// request for the whole object.
func TestObjectRequestWithAPartNumberIsRefusedForReadsToo(t *testing.T) {
	handler, _, _ := sizeChain(t)
	rec := openModeRequest(handler, http.MethodPut, "/mp/reads.bin", strings.NewReader("BODY"), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := openModeRequest(handler, method, "/mp/reads.bin?partNumber=1", nil, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code,
			"%s with partNumber but no uploadId answered %d, want 400: %s", method, rec.Code, rec.Body.String())
	}
}
