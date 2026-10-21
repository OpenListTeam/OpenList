package baidu_netdisk

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

// Baidu error codes handled specially.
const (
	errnoBatchFailed = 12    // filemanager: some items of the batch failed, see info[].errno
	errnoNotExist    = -9    // file or directory does not exist
	errnoPCSNotExist = 31066 // PCS: file does not exist
	errnoRateLimited = 31034 // hit API frequency control
	cooldownBase     = 8 * time.Second
	cooldownMax      = 30 * time.Second
	// maxCooldownWaitPerCall bounds how long a single API call may wait for
	// the breaker. Past that the call fails with a rate-limit error so callers
	// (e.g. S3 clients receiving SlowDown) can back off on their own.
	maxCooldownWaitPerCall = 35 * time.Second
)

// apiItem is one entry of a filemanager response's info list.
type apiItem struct {
	Errno int    `json:"errno"`
	Path  string `json:"path"`
}

// apiError is a non-zero errno returned by the Baidu API.
type apiError struct {
	URL   string
	Errno int
	Items []apiItem // failed items of a filemanager batch, if any
}

func (e *apiError) Error() string {
	if len(e.Items) > 0 {
		return fmt.Sprintf("req: [%s] ,errno: %d, item errno: %d (path %s), refer to https://pan.baidu.com/union/doc/",
			e.URL, e.Errno, e.Items[0].Errno, e.Items[0].Path)
	}
	return fmt.Sprintf("req: [%s] ,errno: %d, refer to https://pan.baidu.com/union/doc/", e.URL, e.Errno)
}

// RateLimited reports whether Baidu rejected the request with frequency control,
// either directly or for any item of a batch.
func (e *apiError) RateLimited() bool {
	if e.Errno == errnoRateLimited {
		return true
	}
	for _, it := range e.Items {
		if it.Errno == errnoRateLimited {
			return true
		}
	}
	return false
}

// NotExist reports whether the request failed only because the target(s) do not exist.
func (e *apiError) NotExist() bool {
	if isNotExistErrno(e.Errno) {
		return true
	}
	if e.Errno != errnoBatchFailed || len(e.Items) == 0 {
		return false
	}
	for _, it := range e.Items {
		if !isNotExistErrno(it.Errno) {
			return false
		}
	}
	return true
}

// Unwrap lets errors.Is(err, errs.TemporaryCapacity) match rate-limit errors,
// which the S3 server maps to 503 SlowDown.
func (e *apiError) Unwrap() error {
	if e.RateLimited() {
		return errs.TemporaryCapacity
	}
	return nil
}

func isNotExistErrno(n int) bool { return n == errnoNotExist || n == errnoPCSNotExist }

// parseAPIError returns nil when body has errno 0. For a failed filemanager
// batch it keeps the failed info[] items, which carry the real cause.
func parseAPIError(furl string, body []byte) *apiError {
	var r struct {
		Errno int       `json:"errno"`
		Info  []apiItem `json:"info"`
	}
	if err := utils.Json.Unmarshal(body, &r); err != nil {
		// keep the previous behaviour for odd bodies: only look at errno
		r.Errno = utils.Json.Get(body, "errno").ToInt()
		r.Info = nil
	}
	if r.Errno == 0 {
		return nil
	}
	e := &apiError{URL: furl, Errno: r.Errno}
	for _, it := range r.Info {
		if it.Errno != 0 {
			e.Items = append(e.Items, it)
		}
	}
	return e
}

func asAPIError(err error) (*apiError, bool) {
	var e *apiError
	ok := errors.As(err, &e)
	return e, ok
}

// outcome of a request made under a permit.
type outcome int

const (
	outcomeSuccess outcome = iota
	outcomeRateLimited
	outcomeOther // any other error, including ctx cancellation
)

// cooldown is a small circuit breaker shared by all requests of one driver
// instance. When Baidu answers 31034 the breaker opens and every request
// pauses. When the pause ends exactly one request is let through as a probe
// while the rest keep waiting:
//   - probe succeeds: the breaker closes and all waiters proceed;
//   - probe gets 31034: the pause is extended (doubling, capped) and the next
//     expiry probes again;
//   - probe fails otherwise or is cancelled: the probe slot passes to the next
//     waiter.
//
// There is no limit while the breaker is closed.
type cooldown struct {
	mu      sync.Mutex
	tripped bool          // breaker open
	until   time.Time     // end of the current pause
	penalty time.Duration // length of the next pause
	probing bool          // a probe is in flight
	changed chan struct{} // closed and replaced on every state change
	probes  int           // probes granted (for tests and logs)

	base, max time.Duration // pause bounds
	maxWait   time.Duration // per-call wait budget

	now    func() time.Time
	jitter func() time.Duration
}

func newCooldown() *cooldown {
	c := &cooldown{
		penalty: cooldownBase,
		base:    cooldownBase,
		max:     cooldownMax,
		maxWait: maxCooldownWaitPerCall,
		changed: make(chan struct{}),
		now:     time.Now,
	}
	// up to 1/8 of the base pause (1s by default), so instances do not resume in lockstep
	c.jitter = func() time.Duration { return time.Duration(rand.Int63n(int64(c.base/8) + 1)) }
	return c
}

// budget is the per-call wait limit.
func (c *cooldown) budget() time.Duration {
	if c == nil {
		return 0
	}
	return c.maxWait
}

// permit is held by one request between acquire and done.
type permit struct {
	c     *cooldown
	probe bool
	once  sync.Once
}

// done reports the request's outcome. It must be called exactly once per
// permit; extra calls are ignored. A nil permit is a no-op.
func (p *permit) done(o outcome) {
	if p == nil || p.c == nil {
		return
	}
	p.once.Do(func() { p.c.report(p.probe, o) })
}

// acquire waits until the request may be sent. It returns ok=false without
// sending once waiting would exceed budget, and ctx's error if ctx ends first.
// waited is the time spent waiting either way.
func (c *cooldown) acquire(ctx context.Context, budget time.Duration) (p *permit, waited time.Duration, ok bool, err error) {
	if c == nil {
		return nil, 0, true, nil
	}
	start := c.now()
	for {
		c.mu.Lock()
		now := c.now()
		waited = now.Sub(start)
		if !c.tripped {
			c.mu.Unlock()
			return &permit{c: c}, waited, true, nil
		}
		wait := c.until.Sub(now) // > 0: pause still running
		if wait <= 0 && !c.probing {
			c.probing = true
			c.probes++
			c.mu.Unlock()
			return &permit{c: c, probe: true}, waited, true, nil
		}
		ch := c.changed
		c.mu.Unlock()

		left := budget - waited
		if left <= 0 || wait > left {
			return nil, waited, false, nil
		}
		if wait <= 0 { // a probe is in flight: wait for its report
			wait = left
		}
		t := time.NewTimer(wait)
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, c.now().Sub(start), false, ctx.Err()
		}
		t.Stop()
	}
}

func (c *cooldown) report(probe bool, o outcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	switch {
	case probe:
		c.probing = false
		switch o {
		case outcomeSuccess:
			c.tripped = false
			c.penalty = c.base
			log.Infof("[baidu_netdisk] rate limit probe succeeded, resuming requests")
		case outcomeRateLimited:
			d := c.pause(now)
			log.Warnf("[baidu_netdisk] rate limit probe still throttled (31034), pausing requests for %s", d.Round(time.Millisecond))
		default:
			log.Debugf("[baidu_netdisk] rate limit probe failed without a verdict, handing over")
		}
		c.broadcast()
	case o == outcomeRateLimited && !c.tripped:
		c.tripped = true
		d := c.pause(now)
		log.Warnf("[baidu_netdisk] hit rate limit (31034), pausing requests for %s", d.Round(time.Millisecond))
		c.broadcast()
	}
	// Other outcomes of non-probe requests, e.g. 31034 for requests sent
	// before the breaker opened, change nothing.
}

// pause starts a new pause and escalates the next one. c.mu must be held.
func (c *cooldown) pause(now time.Time) time.Duration {
	if c.penalty <= 0 {
		c.penalty = c.base
	}
	d := c.penalty + c.jitter()
	c.until = now.Add(d)
	c.penalty *= 2
	if c.penalty > c.max {
		c.penalty = c.max
	}
	return d
}

func (c *cooldown) broadcast() {
	close(c.changed)
	c.changed = make(chan struct{})
}
