package s3

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/gofakes3"
	"github.com/OpenListTeam/gofakes3/s3mem"
)

func TestMapBackendErrorMapsOnlyTemporaryCapacity(t *testing.T) {
	capacity := errs.NewErr(errs.TemporaryCapacity, "callback admission timed out")
	if got := mapBackendError(capacity); got != gofakes3.ErrSlowDown {
		t.Fatalf("capacity error mapped to %v, want %v", got, gofakes3.ErrSlowDown)
	}

	permanent := errors.New("permission denied")
	if got := mapBackendError(permanent); got != permanent {
		t.Fatalf("permanent error mapped to %v, want original error", got)
	}
	if got := mapBackendError(nil); got != nil {
		t.Fatalf("nil error mapped to %v", got)
	}
}

type capacityBackend struct {
	gofakes3.Backend
}

func (b capacityBackend) GetObject(context.Context, string, string, *gofakes3.ObjectRangeRequest) (*gofakes3.Object, error) {
	return nil, mapBackendError(errs.NewErr(errs.TemporaryCapacity, "callback admission timed out"))
}

func TestTemporaryCapacityProducesS3SlowDownResponse(t *testing.T) {
	memory := s3mem.New()
	if err := memory.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(gofakes3.New(capacityBackend{Backend: memory}).Server())
	defer server.Close()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/bucket/object", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
	var result gofakes3.ErrorResult
	if err := xml.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Code != gofakes3.ErrSlowDown || result.Message != gofakes3.ErrSlowDown.Message() {
		t.Fatalf("S3 error = %#v, want SlowDown with standard message", result)
	}
}
