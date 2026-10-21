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
)

// Baidu error codes handled specially.
const (
	errnoBatchFailed = 12    // filemanager: some items of the batch failed, see info[].errno
	errnoNotExist    = -9    // file or directory does not exist
	errnoPCSNotExist = 31066 // PCS: file does not exist
	errnoRateLimited = 31034 // hit API frequency control
	cooldownBase     = 8 * time.Second
	cooldownMax      = 30 * time.Second
	// maxCooldownWaitPerCall bounds how long a single API call may sleep in
	// cooldowns. Past that the call fails with a rate-limit error so callers
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

// cooldown is shared by all requests of one driver instance. When Baidu answers
// 31034 every request pauses until the window passes, instead of each goroutine
// hammering the API with its own retries. There is no limit while not throttled.
type cooldown struct {
	mu      sync.Mutex
	until   time.Time
	penalty time.Duration // next window length

	base, max time.Duration // window bounds
	maxWait   time.Duration // per-call cooldown wait budget

	now    func() time.Time
	jitter func() time.Duration
	sleep  func(ctx context.Context, d time.Duration) error
}

func newCooldown() *cooldown {
	c := &cooldown{
		penalty: cooldownBase,
		base:    cooldownBase,
		max:     cooldownMax,
		maxWait: maxCooldownWaitPerCall,
		now:     time.Now,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	// up to 1/8 of the base window (1s by default), so instances do not resume in lockstep
	c.jitter = func() time.Duration { return time.Duration(rand.Int63n(int64(c.base/8) + 1)) }
	return c
}

// remaining is the time left in the current window.
func (c *cooldown) remaining() time.Duration {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.until.Sub(c.now()); d > 0 {
		return d
	}
	return 0
}

// budget is the per-call cooldown wait limit.
func (c *cooldown) budget() time.Duration {
	if c == nil {
		return 0
	}
	return c.maxWait
}

// wait blocks until the current window ends. It returns false without waiting
// if that would exceed budget.
func (c *cooldown) wait(ctx context.Context, budget time.Duration) (time.Duration, bool, error) {
	if c == nil {
		return 0, true, nil
	}
	d := c.remaining()
	if d <= 0 {
		return 0, true, nil
	}
	if d > budget {
		return 0, false, nil
	}
	return d, true, c.sleep(ctx, d)
}

// hit records a 31034 and returns the time left in the window and whether this
// hit opened it. Hits inside an active window (other in-flight requests of the
// same burst) do not extend it; a hit after the window ended doubles it.
func (c *cooldown) hit() (time.Duration, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if d := c.until.Sub(now); d > 0 {
		return d, false
	}
	if c.penalty <= 0 {
		c.penalty = c.base
	}
	d := c.penalty + c.jitter()
	c.until = now.Add(d)
	c.penalty *= 2
	if c.penalty > c.max {
		c.penalty = c.max
	}
	return d, true
}

// success resets the escalation once a request succeeds outside a window.
func (c *cooldown) success() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.now().Before(c.until) {
		c.penalty = c.base
	}
}
