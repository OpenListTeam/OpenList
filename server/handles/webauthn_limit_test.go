package handles

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLimitPasskeyResponseBody(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/authn/webauthn_finish_login",
		strings.NewReader(strings.Repeat("x", maxPasskeyResponseBytes+1)),
	)

	limitPasskeyResponseBody(context)
	if _, err := io.ReadAll(context.Request.Body); err == nil {
		t.Fatal("oversized passkey response was accepted")
	} else {
		var tooLarge *http.MaxBytesError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("oversized passkey response error = %v, want MaxBytesError", err)
		}
	}
}
