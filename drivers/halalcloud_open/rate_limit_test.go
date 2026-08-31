package halalcloudopen

import (
	"context"
	"net/http"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHalalCloudAPILimiterSharedByCredentials(t *testing.T) {
	first := halalCloudAPILimiter("RATE-LIMIT-TEST.INVALID", t.Name())
	second := halalCloudAPILimiter("rate-limit-test.invalid", t.Name())
	if first != second {
		t.Fatal("same host and client ID did not share an API limiter")
	}

	differentClient := halalCloudAPILimiter("rate-limit-test.invalid", t.Name()+"-other")
	if first == differentClient {
		t.Fatal("different client IDs unexpectedly shared an API limiter")
	}
}

func TestRateLimitedTransportHonorsCanceledContext(t *testing.T) {
	limiter := rate.NewLimiter(rate.Every(time.Hour), 1)
	if !limiter.Allow() {
		t.Fatal("failed to consume the limiter's initial token")
	}

	baseCalled := false
	transport := &rateLimitedTransport{
		base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			baseCalled = true
			return &http.Response{StatusCode: http.StatusOK}, nil
		}),
		limiter: limiter,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://rate-limit-test.invalid", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}

	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip() error = nil, want canceled-context error")
	}
	if baseCalled {
		t.Fatal("RoundTrip() called the base transport after limiter wait failed")
	}
}
