package thunderx

import (
	"errors"
	"net/http"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
)

func TestRequestReturnsErrorWhenTokenIsMissing(t *testing.T) {
	xc := &XunLeiXCommon{}

	_, err := xc.Request(API_URL+"/about", http.MethodGet, nil, nil)
	if !errors.Is(err, errs.EmptyToken) {
		t.Fatalf("expected EmptyToken, got %v", err)
	}
}
