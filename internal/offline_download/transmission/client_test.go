package transmission

import (
	"context"
	stderrors "errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/hekmon/transmissionrpc/v3"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRemoveWithTimeoutDetachesCancellationAndStopsAtDeadline(t *testing.T) {
	requestStarted := false
	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestStarted = true
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}
	endpoint, err := url.Parse("http://transmission.invalid/rpc")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	client, err := transmissionrpc.New(endpoint, &transmissionrpc.Config{CustomClient: httpClient})
	if err != nil {
		t.Fatalf("transmissionrpc.New() error = %v", err)
	}

	parentCtx, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	transmission := &Transmission{client: client}

	started := time.Now()
	err = transmission.removeWithTimeout(parentCtx, 1, 25*time.Millisecond)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("removeWithTimeout() took %s, want a bounded cleanup", elapsed)
	}
	if !requestStarted {
		t.Fatal("removeWithTimeout() did not detach from the canceled parent context")
	}
	if !stderrors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("removeWithTimeout() error = %v, want context.DeadlineExceeded", err)
	}
}
