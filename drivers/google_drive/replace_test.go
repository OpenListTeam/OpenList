package google_drive

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReplacePublishesSourceBeforeRetiringDestination(t *testing.T) {
	oldClient := base.RestyClient
	oldDelay := replaceCleanupDelay
	replaceCleanupDelay = 10 * time.Millisecond
	client := resty.New()
	base.RestyClient = client
	t.Cleanup(func() {
		base.RestyClient = oldClient
		replaceCleanupDelay = oldDelay
	})

	var mu sync.Mutex
	var requests []string
	deleted := make(chan struct{}, 1)
	client.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path+" "+string(body))
		mu.Unlock()
		if r.Method == http.MethodDelete {
			select {
			case deleted <- struct{}{}:
			default:
			}
		}
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

	mu.Lock()
	immediate := append([]string(nil), requests...)
	mu.Unlock()
	if len(immediate) != 2 {
		t.Fatalf("got %d immediate requests, want 2: %v", len(immediate), immediate)
	}
	if got := immediate[0]; !strings.HasPrefix(got, "PATCH /drive/v3/files/source-id ") || !strings.Contains(got, `"name":"canonical"`) {
		t.Fatalf("first request must publish source under canonical name, got %q", got)
	}
	if got := immediate[1]; !strings.HasPrefix(got, "PATCH /drive/v3/files/destination-id ") || !strings.Contains(got, `"name":".openlist-replaced-destination-id"`) {
		t.Fatalf("second request must retire old destination under tombstone name, got %q", got)
	}

	select {
	case <-deleted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for asynchronous destination cleanup")
	}
	mu.Lock()
	defer mu.Unlock()
	if got := requests[len(requests)-1]; got != "DELETE /drive/v3/files/destination-id " {
		t.Fatalf("final request must delete retired destination by id, got %q", got)
	}
}
