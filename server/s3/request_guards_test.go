package s3

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPresignedLifetimeGuardAppliesToHeaderSignedRequests(t *testing.T) {
	// A V4 header-signed request is not a presigned URL, but gofakes3 reads
	// X-Amz-Expires on every request, so the ceiling has to apply here too.
	handler := presignedLifetimeGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req, _ := http.NewRequest("GET", "/priv/obj?X-Amz-Expires=999999999", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app-key/20261028/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=xxx")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code, "a header-signed request must not escape the expiry ceiling")

	// A timestamp far in the future is refused too, in the header or the query.
	skewed, _ := http.NewRequest("GET", "/priv/obj", nil)
	skewed.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	skewed.Header.Set("X-Amz-Date", time.Now().Add(1500*24*time.Hour).UTC().Format("20060102T150405Z"))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, skewed)
	assert.Equal(t, http.StatusForbidden, rec.Code, "a signature dated far in the future must be refused")

	// The control case still passes through.
	ok, _ := http.NewRequest("GET", "/priv/obj?X-Amz-Expires=3600", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, ok)
	assert.Equal(t, http.StatusOK, rec.Code)
}
