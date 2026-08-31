package halalcloudopen

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const halalCloudAPIRequestInterval = time.Second

// HalalCloud applies its quota to an APP key rather than an individual HTTP
// client. Keep limiters process-wide so mounting the same credentials more than
// once cannot accidentally multiply the permitted request rate.
var halalCloudAPILimiters sync.Map

func halalCloudAPILimiter(host, clientID string) *rate.Limiter {
	key := strings.ToLower(strings.TrimSpace(host)) + "\x00" + strings.TrimSpace(clientID)
	limiter, _ := halalCloudAPILimiters.LoadOrStore(
		key,
		// Personal API credentials are limited to one request per second. The
		// driver has no reliable way to distinguish them from public APP keys,
		// so use the documented conservative limit for both.
		rate.NewLimiter(rate.Every(halalCloudAPIRequestInterval), 1),
	)
	return limiter.(*rate.Limiter)
}

// rateLimitedTransport enforces the provider quota below the SDK service
// layer. This covers offline-task pagination and concurrent file/API requests
// with one shared gate instead of throttling each service independently.
type rateLimitedTransport struct {
	base    http.RoundTripper
	limiter *rate.Limiter
}

func (t *rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.wait(req.Context()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

func (t *rateLimitedTransport) wait(ctx context.Context) error {
	return t.limiter.Wait(ctx)
}
