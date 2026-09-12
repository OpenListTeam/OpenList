package halalcloudopen

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const halalCloudAPIRequestInterval = time.Second

// API quotas are shared by mounts that use the same credentials.
var halalCloudAPILimiters sync.Map

func halalCloudAPILimiter(host, clientID string) *rate.Limiter {
	key := strings.ToLower(strings.TrimSpace(host)) + "\x00" + strings.TrimSpace(clientID)
	limiter, _ := halalCloudAPILimiters.LoadOrStore(
		key,
		rate.NewLimiter(rate.Every(halalCloudAPIRequestInterval), 1),
	)
	return limiter.(*rate.Limiter)
}

// rateLimitedTransport applies the credential quota to every SDK service.
type rateLimitedTransport struct {
	base    http.RoundTripper
	limiter *rate.Limiter
}

func (t *rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.limiter.Wait(req.Context()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}
