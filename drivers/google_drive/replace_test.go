package google_drive

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReplacePublishesSourceBeforeDeletingDestination(t *testing.T) {
	oldClient := base.RestyClient
	client := resty.New()
	base.RestyClient = client
	t.Cleanup(func() { base.RestyClient = oldClient })

	var mu sync.Mutex
	var requests []string
	client.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path+" "+string(body))
		mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    r,
		}, nil
	}))

	d := &GoogleDrive{}
	src := &model.Object{ID: "source-id", Name: "staged"}
	dst := &model.Object{ID: "destination-id", Name: "canonical"}
	if err := d.Replace(context.Background(), src, dst, "canonical"); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2: %v", len(requests), requests)
	}
	if got := requests[0]; !strings.HasPrefix(got, "PATCH /drive/v3/files/source-id ") || !strings.Contains(got, `"name":"canonical"`) {
		t.Fatalf("first request must publish source under canonical name, got %q", got)
	}
	if got := requests[1]; got != "DELETE /drive/v3/files/destination-id " {
		t.Fatalf("second request must delete old destination by id, got %q", got)
	}
}
