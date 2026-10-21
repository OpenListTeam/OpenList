package baidu_netdisk

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

// Real body captured from Baidu while deleting quickly (path shortened).
const rateLimitedBatchBody = `{"errno":12,"info":[{"errno":31034,"path":"\/alist\/x\/q0120"}],"request_id":9111630116005502295}`

func TestParseAPIError(t *testing.T) {
	cases := []struct {
		name               string
		body               string
		wantNil            bool
		wantRL, wantNotExt bool
	}{
		{name: "success", body: `{"errno":0,"info":[{"errno":0,"path":"/a"}]}`, wantNil: true},
		{name: "no errno field", body: `{"list":[]}`, wantNil: true},
		{name: "batch rate limited", body: rateLimitedBatchBody, wantRL: true},
		{name: "outer rate limited", body: `{"errno":31034}`, wantRL: true},
		{name: "batch not exist", body: `{"errno":12,"info":[{"errno":-9,"path":"/a"}]}`, wantNotExt: true},
		{name: "batch pcs not exist", body: `{"errno":12,"info":[{"errno":31066,"path":"/a"}]}`, wantNotExt: true},
		{name: "outer not exist", body: `{"errno":-9}`, wantNotExt: true},
		{name: "batch mixed", body: `{"errno":12,"info":[{"errno":-9,"path":"/a"},{"errno":-7,"path":"/b"}]}`},
		{name: "batch without info", body: `{"errno":12}`},
		{name: "token", body: `{"errno":-6}`},
		{name: "info not a list", body: `{"errno":2,"info":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := parseAPIError("u", []byte(c.body))
			if c.wantNil {
				if e != nil {
					t.Fatalf("want nil, got %v", e)
				}
				return
			}
			if e == nil {
				t.Fatal("want error, got nil")
			}
			if e.RateLimited() != c.wantRL {
				t.Errorf("RateLimited = %v, want %v", e.RateLimited(), c.wantRL)
			}
			if e.NotExist() != c.wantNotExt {
				t.Errorf("NotExist = %v, want %v", e.NotExist(), c.wantNotExt)
			}
			if got := errors.Is(e, errs.TemporaryCapacity); got != c.wantRL {
				t.Errorf("errors.Is(TemporaryCapacity) = %v, want %v", got, c.wantRL)
			}
		})
	}
	e := parseAPIError("https://pan.baidu.com/rest/2.0/xpan/file", []byte(rateLimitedBatchBody))
	if !strings.Contains(e.Error(), "31034") || !strings.Contains(e.Error(), "q0120") {
		t.Errorf("message should carry item errno and path: %s", e.Error())
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }
func (f *fakeClock) add(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func newTestCooldown(clk *fakeClock) *cooldown {
	c := newCooldown()
	c.now = clk.now
	c.jitter = func() time.Duration { return 0 }
	c.sleep = func(ctx context.Context, d time.Duration) error { clk.add(d); return ctx.Err() }
	return c
}

func TestCooldownEscalatesOncePerWindowAndResets(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := newTestCooldown(clk)

	if d, _ := c.hit(); d != 8*time.Second {
		t.Fatalf("first hit = %s, want 8s", d)
	}
	// a burst of in-flight requests hitting 31034 must not extend or escalate
	for i := 0; i < 15; i++ {
		clk.add(100 * time.Millisecond)
		c.hit()
	}
	if r := c.remaining(); r != 8*time.Second-1500*time.Millisecond {
		t.Fatalf("remaining after burst = %s", r)
	}
	clk.add(c.remaining())
	if d, _ := c.hit(); d != 16*time.Second {
		t.Fatalf("hit right after window = %s, want 16s", d)
	}
	clk.add(16 * time.Second)
	if d, _ := c.hit(); d != 30*time.Second {
		t.Fatalf("third hit = %s, want 30s (cap)", d)
	}
	clk.add(30 * time.Second)
	if d, _ := c.hit(); d != 30*time.Second {
		t.Fatalf("fourth hit = %s, want 30s (cap)", d)
	}
	// success inside a window keeps the escalation
	clk.add(time.Second)
	c.success()
	clk.add(29 * time.Second)
	c.success()
	if d, _ := c.hit(); d != 8*time.Second {
		t.Fatalf("hit after success = %s, want reset to 8s", d)
	}
}

func TestCooldownWaitBudgetAndContext(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := newTestCooldown(clk)
	if w, ok, err := c.wait(context.Background(), time.Second); w != 0 || !ok || err != nil {
		t.Fatalf("no window: got %s %v %v", w, ok, err)
	}
	c.hit()
	if _, ok, _ := c.wait(context.Background(), 5*time.Second); ok {
		t.Fatal("wait beyond budget should refuse")
	}
	if w, ok, err := c.wait(context.Background(), 10*time.Second); w != 8*time.Second || !ok || err != nil {
		t.Fatalf("wait = %s %v %v, want 8s", w, ok, err)
	}

	real := newCooldown()
	real.hit()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, _, err := real.wait(ctx, time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want ctx error, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("wait ignored context cancellation")
	}

	var nilC *cooldown
	if _, ok, err := nilC.wait(context.Background(), 0); !ok || err != nil {
		t.Fatal("nil cooldown must be a no-op")
	}
	nilC.hit()
	nilC.success()
}

// fakeBaidu answers filemanager calls: 31034 for every request while the
// throttle window is open, then success. Deleting an unknown path gives -9.
type fakeBaidu struct {
	mu        sync.Mutex
	openUntil time.Time
	window    time.Duration
	threshold int // requests allowed before the window opens
	calls     atomic.Int64
	throttled atomic.Int64
	missing   string
}

func (f *fakeBaidu) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	_ = r.ParseForm()
	body := `{"errno":0,"info":[{"errno":0,"path":"/x"}],"request_id":1}`
	f.mu.Lock()
	now := time.Now()
	if f.missing != "" && strings.Contains(r.PostForm.Get("filelist"), f.missing) {
		body = `{"errno":12,"info":[{"errno":-9,"path":"/missing"}],"request_id":1}`
	} else if now.Before(f.openUntil) {
		body = rateLimitedBatchBody
		f.throttled.Add(1)
	} else if f.threshold > 0 {
		f.threshold--
		if f.threshold == 0 {
			f.openUntil = now.Add(f.window)
		}
	}
	f.mu.Unlock()
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func withFakeBaidu(t *testing.T, f *fakeBaidu) {
	t.Helper()
	old := base.RestyClient
	base.RestyClient = resty.New().SetTransport(f)
	t.Cleanup(func() { base.RestyClient = old })
}

func testDriver(base, max, maxWait time.Duration) *BaiduNetdisk {
	d := &BaiduNetdisk{cooldown: newCooldown()}
	d.cooldown.base, d.cooldown.penalty, d.cooldown.max, d.cooldown.maxWait = base, base, max, maxWait
	return d
}

func TestRemoveRidesOutRateLimitWindow(t *testing.T) {
	f := &fakeBaidu{threshold: 20, window: 400 * time.Millisecond}
	withFakeBaidu(t, f)
	d := testDriver(150*time.Millisecond, time.Second, 3*time.Second)

	const n, conc = 200, 32
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var failed atomic.Int64
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			obj := &model.Object{Path: "/x"}
			if err := d.Remove(context.Background(), obj); err != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	t.Logf("%d deletes, %d calls to baidu, %d throttled, %s", n, f.calls.Load(), f.throttled.Load(), time.Since(start))
	if failed.Load() != 0 {
		t.Fatalf("%d deletes failed", failed.Load())
	}
	if f.throttled.Load() == 0 {
		t.Fatal("test did not exercise the window")
	}
	// without the shared cooldown each of the 32 workers keeps retrying on its
	// own; with it, the burst is paused together
	if f.throttled.Load() > 3*conc {
		t.Fatalf("too many requests during the window: %d", f.throttled.Load())
	}
}

func TestRemoveReturnsRateLimitErrorWhenWindowOutlastsBudget(t *testing.T) {
	f := &fakeBaidu{openUntil: time.Now().Add(time.Hour)}
	withFakeBaidu(t, f)
	d := testDriver(100*time.Millisecond, 200*time.Millisecond, 350*time.Millisecond)

	start := time.Now()
	err := d.Remove(context.Background(), &model.Object{Path: "/x"})
	if !errors.Is(err, errs.TemporaryCapacity) {
		t.Fatalf("want rate-limit error, got %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("call blocked too long: %s", el)
	}
	if c := f.calls.Load(); c != 3 {
		t.Fatalf("calls = %d, want 3 (initial + 2 retries after cooldowns)", c)
	}
}

func TestRemoveTreatsMissingAsSuccess(t *testing.T) {
	f := &fakeBaidu{missing: "gone"}
	withFakeBaidu(t, f)
	d := testDriver(time.Second, time.Second, time.Second)
	if err := d.Remove(context.Background(), &model.Object{Path: "/gone"}); err != nil {
		t.Fatalf("remove of missing file: %v", err)
	}
	if c := f.calls.Load(); c != 1 {
		t.Fatalf("missing file should not be retried, calls = %d", c)
	}
}

func TestRemoveHonorsContextDuringCooldown(t *testing.T) {
	f := &fakeBaidu{openUntil: time.Now().Add(time.Hour)}
	withFakeBaidu(t, f)
	d := testDriver(10*time.Second, 10*time.Second, 30*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := d.Remove(ctx, &model.Object{Path: "/x"})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v after %s, want quick ctx error", err, time.Since(start))
	}
}
