package s3

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
)

// multiDeleteDigestGuard verifies the Content-MD5 a DeleteObjects request
// declares. S3 requires the header on `POST /bucket?delete` precisely so that a
// body damaged in transit is refused instead of deleting the wrong keys, but
// gofakes3 reads the key list and never looks at the digest: a body rewritten
// between signing and delivery was executed as if it were the one the client
// composed.
//
// The body is small (limitMultiDeleteBody caps it) and has to be inspected
// before the keys are acted upon, so it is buffered and put back.
func multiDeleteDigestGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := r.URL.Query()["delete"]; !ok {
			next.ServeHTTP(w, r)
			return
		}
		declared := strings.TrimSpace(r.Header.Get("Content-MD5"))
		if declared == "" {
			// S3 requires the header; refusing a request without it would break
			// clients that were already working against this gateway, so only a
			// digest that is present is enforced.
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err != nil {
			writeS3Error(w, r, http.StatusBadRequest, "IncompleteBody", bucketOnlyName(r.URL.Path),
				"The request body ended before the whole document arrived.")
			return
		}
		sum := md5.Sum(body)
		if base64.StdEncoding.EncodeToString(sum[:]) != declared {
			writeS3Error(w, r, http.StatusBadRequest, "InvalidDigest", bucketOnlyName(r.URL.Path),
				"The Content-MD5 you specified did not match what we received.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
