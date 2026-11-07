package s3

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// "a//b" and "a/b" are two different keys in S3, but path.Join collapsed the
// empty segment, so both resolved to one file: a PUT of either overwrote the
// other and a GET of either returned the surviving bytes. Refusing the key keeps
// the merge from happening silently.
func TestEmptyPathSegmentIsRefusedInsteadOfAliasing(t *testing.T) {
	handler := openModeChain(t)

	seed := openModeRequest(handler, http.MethodPut, "/mp/seg/plain.txt", strings.NewReader("PLAIN"), nil)
	require.Equal(t, http.StatusOK, seed.Code, seed.Body.String())

	rec := openModeRequest(handler, http.MethodPut, "/mp/seg//plain.txt", strings.NewReader("ALIAS"), nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "InvalidArgument")

	read := openModeRequest(handler, http.MethodGet, "/mp/seg/plain.txt", nil, nil)
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	assert.Equal(t, "PLAIN", read.Body.String(), "the aliased write must not replace the real key")

	head := openModeRequest(handler, http.MethodHead, "/mp/seg//plain.txt", nil, nil)
	assert.Equal(t, http.StatusBadRequest, head.Code, "reads must refuse the key as well")
}

// "marker.txt/" and "marker.txt" are two keys in S3. Only a directory marker is
// ever stored under the former here, so a read that names it must answer
// NoSuchKey instead of the bytes of the same-named file.
func TestTrailingSlashReadDoesNotServeTheSameNamedFile(t *testing.T) {
	handler := openModeChain(t)

	seed := openModeRequest(handler, http.MethodPut, "/mp/marker.txt", strings.NewReader("FILE"), nil)
	require.Equal(t, http.StatusOK, seed.Code, seed.Body.String())

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := openModeRequest(handler, method, "/mp/marker.txt/", nil, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, "%s must not answer with the plain key", method)
		assert.NotContains(t, rec.Body.String(), "FILE")
	}
}

// The same distinction matters for destruction: a DELETE of "marker.txt/" used
// to remove the file "marker.txt", a key the request never named.
func TestTrailingSlashDeleteDoesNotRemoveTheSameNamedFile(t *testing.T) {
	handler := openModeChain(t)

	seed := openModeRequest(handler, http.MethodPut, "/mp/keeper.txt", strings.NewReader("KEEP"), nil)
	require.Equal(t, http.StatusOK, seed.Code, seed.Body.String())

	del := openModeRequest(handler, http.MethodDelete, "/mp/keeper.txt/", nil, nil)
	assert.Equal(t, http.StatusNoContent, del.Code, del.Body.String())

	head := openModeRequest(handler, http.MethodHead, "/mp/keeper.txt", nil, nil)
	assert.Equal(t, http.StatusOK, head.Code, "the plain key must survive a delete of its marker form")
}

// A copy source is a key like any other: copying "marker.txt/" must not read the
// bytes of "marker.txt".
func TestCopySourceWithTrailingSlashDoesNotReadTheFile(t *testing.T) {
	handler := openModeChain(t)

	seed := openModeRequest(handler, http.MethodPut, "/mp/copysrc.txt", strings.NewReader("SOURCE"), nil)
	require.Equal(t, http.StatusOK, seed.Code, seed.Body.String())

	rec := openModeRequest(handler, http.MethodPut, "/mp/copydst.txt", nil,
		map[string]string{"X-Amz-Copy-Source": "/mp/copysrc.txt/"})
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	head := openModeRequest(handler, http.MethodHead, "/mp/copydst.txt", nil, nil)
	assert.Equal(t, http.StatusNotFound, head.Code)
}
