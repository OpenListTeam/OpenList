package teldrive_v2

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
)

func TestNormalizePath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"  ", "/"},
		{"a", "/a"},
		{"/a/b", "/a/b"},
		{"/a/b/", "/a/b"},
		{"//a///b//", "/a/b"},
		{"/a/./b", "/a/b"},
		{"/a/../b", "/b"},
		{"  /a/b  ", "/a/b"},
	} {
		if got := normalizePath(tc.in); got != tc.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The server accepts any preferredPartSize it is given, so these bounds are the
// only thing keeping parts hash-aligned and small enough for Telegram.
func TestNormalizeChunkSize(t *testing.T) {
	for _, tc := range []struct{ in, want int64 }{
		{0, defaultChunkMiB},
		{-5, defaultChunkMiB},
		{16, 16},
		{512, 512},
		{1, 16},      // below one tree-hash block
		{10, 16},     // rounded up to a block boundary
		{17, 32},     // ditto
		{100, 112},   // ditto
		{2000, 2000}, // exactly the ceiling
		{2001, maxChunkMiB},
		{5000, maxChunkMiB},
	} {
		got := normalizeChunkSize(tc.in)
		if got != tc.want {
			t.Errorf("normalizeChunkSize(%d) = %d, want %d", tc.in, got, tc.want)
		}
		if got%treeHashBlockMiB != 0 {
			t.Errorf("normalizeChunkSize(%d) = %d, which is not a multiple of %d", tc.in, got, treeHashBlockMiB)
		}
		if got > maxChunkMiB {
			t.Errorf("normalizeChunkSize(%d) = %d, above the %d ceiling", tc.in, got, maxChunkMiB)
		}
	}
}

// The value arrives from a select, but a hand-edited config or an older storage
// can still carry anything at all, and none of it may take the storage down.
func TestParseShareExpire(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"1", time.Hour},
		{"24", 24 * time.Hour},
		{"168", 168 * time.Hour},
		{" 6 ", 6 * time.Hour},
		{"", defaultShareExpireHours * time.Hour},
		{"0", defaultShareExpireHours * time.Hour},
		{"-3", defaultShareExpireHours * time.Hour},
		{"abc", defaultShareExpireHours * time.Hour},
		{"1.5", defaultShareExpireHours * time.Hour},
	} {
		if got := parseShareExpire(tc.in); got != tc.want {
			t.Errorf("parseShareExpire(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// teldrive collapses several documented statuses onto 409, so the whole 4xx
// family has to count as final; only overload and server faults are worth
// replaying.
func TestRetryable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   bool
	}{
		{"bad request", http.StatusBadRequest, false},
		{"unauthorized", http.StatusUnauthorized, false},
		{"not found", http.StatusNotFound, false},
		{"part conflict", http.StatusConflict, false},
		{"gone", http.StatusGone, false},
		{"hash mismatch", http.StatusUnprocessableEntity, false},
		{"rate limited", http.StatusTooManyRequests, true},
		{"internal", http.StatusInternalServerError, true},
		{"bad gateway", http.StatusBadGateway, true},
		{"unavailable", http.StatusServiceUnavailable, true},
	} {
		err := &ErrResp{status: tc.status}
		if got := retryable(err); got != tc.want {
			t.Errorf("%s (%d): retryable = %v, want %v", tc.name, tc.status, got, tc.want)
		}
	}

	// A transport failure never reached the server, so it says nothing about
	// whether the request would be accepted.
	if !retryable(http.ErrHandlerTimeout) {
		t.Error("a non-API error should be retryable")
	}
}

// Every mutating v2 endpoint rejects a request without this header, with a
// message that points at the body rather than the header.
func TestIdempotentSetsAStableKey(t *testing.T) {
	inner := false
	cb := idempotent(func(*resty.Request) { inner = true })

	first := resty.New().R()
	cb(first)
	key := first.Header.Get("Idempotency-Key")
	if key == "" {
		t.Fatal("Idempotency-Key was not set")
	}
	if !inner {
		t.Error("the wrapped callback did not run")
	}

	// A retry of the same logical operation must reuse the key.
	second := resty.New().R()
	cb(second)
	if got := second.Header.Get("Idempotency-Key"); got != key {
		t.Errorf("key changed between attempts: %q then %q", key, got)
	}

	// A different operation must not.
	other := resty.New().R()
	idempotent(nil)(other)
	if got := other.Header.Get("Idempotency-Key"); got == key {
		t.Error("two operations shared an idempotency key")
	}
}

func TestErrRespMessage(t *testing.T) {
	e := &ErrResp{status: http.StatusConflict}
	e.Detail.Code = "conflict"
	e.Detail.Message = "operation conflicts with current state"

	msg := e.Error()
	for _, want := range []string{"409", "conflict", "operation conflicts"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
	if e.IsNotFound() {
		t.Error("a 409 must not report as not-found")
	}
	if !(&ErrResp{status: http.StatusNotFound}).IsNotFound() {
		t.Error("a 404 must report as not-found")
	}
}

// TestSweepSharesDropsLapsedEntries pins the bound on shareCache: a share that
// has run out is not left in the map for the lifetime of the storage.
func TestSweepShares(t *testing.T) {
	now := time.Now().UTC()
	d := &TeldriveV2{shareCache: &sync.Map{}}
	d.shareCache.Store("live", &cachedShare{token: "a", expiresAt: now.Add(time.Hour)})
	d.shareCache.Store("lapsed", &cachedShare{token: "b", expiresAt: now.Add(-time.Minute)})
	// Inside the renew margin: unusable, so it should go too.
	d.shareCache.Store("lapsing", &cachedShare{token: "c", expiresAt: now.Add(shareRenewMargin / 2)})

	d.sweepShares(now)

	for _, tc := range []struct {
		key  string
		want bool
	}{{"live", true}, {"lapsed", false}, {"lapsing", false}} {
		if _, ok := d.shareCache.Load(tc.key); ok != tc.want {
			t.Errorf("after sweep, %q present = %v, want %v", tc.key, ok, tc.want)
		}
	}

	// A second sweep inside the margin must be skipped, not repeated.
	d.shareCache.Store("added", &cachedShare{token: "d", expiresAt: now.Add(-time.Minute)})
	d.sweepShares(now)
	if _, ok := d.shareCache.Load("added"); !ok {
		t.Error("sweep ran again within shareRenewMargin; it should be rate limited")
	}
}
