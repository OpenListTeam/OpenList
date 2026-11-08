package baidu_netdisk

import (
	"context"
	"errors"
	"io"
	"net/http"
	"runtime"
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

func newTestBreaker(base, max, maxWait time.Duration) *cooldown {
	c := newCooldown()
	c.base, c.penalty, c.max, c.maxWait = base, base, max, maxWait
	c.jitter = func() time.Duration { return 0 }
	return c
}

func trip(c *cooldown) { c.report(false, outcomeRateLimited) }

func checkNoLeak(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("goroutines: %d before, %d after", before, n)
	}
}

// probeScenario starts n waiters on a tripped breaker. Each probe reports the
// next verdict from verdicts (the last one repeats); non-probe holders report
// success. It records grants, non-probe requests let through before the
// successful probe reported (early) and overlapping probes (maxInflight).
type probeScenario struct {
	mu          sync.Mutex
	probeGrants []time.Time
	probeDone   []time.Time
	released    time.Time
	early       int
	inflight    int
	maxInflight int
	granted     int
}

func runProbeScenario(t *testing.T, c *cooldown, n int, hold time.Duration, verdicts ...outcome) *probeScenario {
	t.Helper()
	sc := &probeScenario{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, _, ok, err := c.acquire(context.Background(), 10*time.Second)
			if !ok || err != nil {
				t.Errorf("acquire: ok=%v err=%v", ok, err)
				return
			}
			sc.mu.Lock()
			sc.granted++
			if p.probe {
				sc.probeGrants = append(sc.probeGrants, time.Now())
				sc.inflight++
				if sc.inflight > sc.maxInflight {
					sc.maxInflight = sc.inflight
				}
				v := verdicts[min(len(sc.probeGrants)-1, len(verdicts)-1)]
				sc.mu.Unlock()
				time.Sleep(hold)
				sc.mu.Lock()
				sc.inflight--
				sc.probeDone = append(sc.probeDone, time.Now())
				if v == outcomeSuccess {
					sc.released = time.Now()
				}
				sc.mu.Unlock()
				p.done(v)
				return
			}
			if sc.released.IsZero() {
				sc.early++
			}
			sc.mu.Unlock()
			p.done(outcomeSuccess)
		}()
	}
	wg.Wait()
	return sc
}

func TestBreakerLetsOneProbeThroughThenReleasesAll(t *testing.T) {
	before := runtime.NumGoroutine()
	c := newTestBreaker(100*time.Millisecond, time.Second, 10*time.Second)
	start := time.Now()
	trip(c)
	sc := runProbeScenario(t, c, 32, 50*time.Millisecond, outcomeSuccess)
	if len(sc.probeGrants) != 1 || sc.maxInflight != 1 {
		t.Fatalf("probes=%d maxInflight=%d, want exactly 1", len(sc.probeGrants), sc.maxInflight)
	}
	if sc.early != 0 {
		t.Fatalf("%d requests went out before the probe succeeded", sc.early)
	}
	if sc.granted != 32 {
		t.Fatalf("granted %d of 32", sc.granted)
	}
	if d := sc.probeGrants[0].Sub(start); d < 100*time.Millisecond {
		t.Fatalf("probe after %s, before the pause ended", d)
	}
	if c.tripped || c.penalty != c.base {
		t.Fatalf("breaker should be closed and reset: tripped=%v penalty=%s", c.tripped, c.penalty)
	}
	checkNoLeak(t, before)
}

func TestBreakerThrottledProbeRearmsPause(t *testing.T) {
	before := runtime.NumGoroutine()
	c := newTestBreaker(60*time.Millisecond, time.Second, 10*time.Second)
	trip(c) // pause 60ms, next 120ms
	sc := runProbeScenario(t, c, 32, 20*time.Millisecond, outcomeRateLimited, outcomeRateLimited, outcomeSuccess)
	if len(sc.probeGrants) != 3 || sc.maxInflight != 1 || sc.early != 0 || sc.granted != 32 {
		t.Fatalf("probes=%d maxInflight=%d early=%d granted=%d", len(sc.probeGrants), sc.maxInflight, sc.early, sc.granted)
	}
	for i, want := range []time.Duration{120 * time.Millisecond, 240 * time.Millisecond} {
		if gap := sc.probeGrants[i+1].Sub(sc.probeDone[i]); gap < want {
			t.Fatalf("probe %d came %s after the throttled probe, want >= %s", i+2, gap, want)
		}
	}
	checkNoLeak(t, before)
}

func TestBreakerHandsProbeOverOnFailure(t *testing.T) {
	before := runtime.NumGoroutine()
	c := newTestBreaker(50*time.Millisecond, time.Second, 10*time.Second)
	trip(c)
	// the first probe fails without a verdict (ctx cancelled / network error)
	sc := runProbeScenario(t, c, 32, 10*time.Millisecond, outcomeOther, outcomeSuccess)
	if len(sc.probeGrants) != 2 || sc.maxInflight != 1 || sc.early != 0 || sc.granted != 32 {
		t.Fatalf("probes=%d maxInflight=%d early=%d granted=%d", len(sc.probeGrants), sc.maxInflight, sc.early, sc.granted)
	}
	if gap := sc.probeGrants[1].Sub(sc.probeDone[0]); gap > 30*time.Millisecond {
		t.Fatalf("probe handed over after %s, want immediately", gap)
	}
	checkNoLeak(t, before)
}

func TestBreakerCancelledProbeHolderHandsOver(t *testing.T) {
	c := newTestBreaker(20*time.Millisecond, time.Second, 10*time.Second)
	trip(c)
	time.Sleep(30 * time.Millisecond)
	p, _, ok, _ := c.acquire(context.Background(), time.Second)
	if !ok || !p.probe {
		t.Fatal("expected to become the probe")
	}
	got := make(chan *permit)
	go func() {
		q, _, _, _ := c.acquire(context.Background(), time.Second)
		got <- q
	}()
	select {
	case <-got:
		t.Fatal("second request must wait while a probe is in flight")
	case <-time.After(50 * time.Millisecond):
	}
	p.done(outcomeOther) // e.g. the probe's ctx was cancelled
	p.done(outcomeSuccess)
	select {
	case q := <-got:
		if q == nil || !q.probe {
			t.Fatal("waiter should take over the probe")
		}
		if !c.tripped {
			t.Fatal("extra done() calls must be ignored")
		}
		q.done(outcomeSuccess)
	case <-time.After(time.Second):
		t.Fatal("probe slot was not handed over")
	}
}

func TestBreakerBudgetAndContext(t *testing.T) {
	before := runtime.NumGoroutine()
	c := newTestBreaker(10*time.Second, 30*time.Second, 35*time.Second)
	if p, _, ok, err := c.acquire(context.Background(), time.Second); !ok || err != nil || p.probe {
		t.Fatal("closed breaker must let requests through")
	} else {
		p.done(outcomeSuccess)
	}
	trip(c)
	start := time.Now()
	if _, _, ok, err := c.acquire(context.Background(), time.Second); ok || err != nil {
		t.Fatalf("pause beyond budget: ok=%v err=%v", ok, err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("pause beyond budget should fail fast")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, _, err := c.acquire(ctx, time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want ctx error, got %v", err)
	}

	// a probe that never reports: waiters give up at their budget
	c2 := newTestBreaker(10*time.Millisecond, time.Second, time.Second)
	trip(c2)
	time.Sleep(20 * time.Millisecond)
	p, _, _, _ := c2.acquire(context.Background(), time.Second)
	start = time.Now()
	_, w, ok, err := c2.acquire(context.Background(), 100*time.Millisecond)
	if ok || err != nil || w < 90*time.Millisecond || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("waiting on a stuck probe: ok=%v err=%v waited=%s", ok, err, w)
	}
	p.done(outcomeSuccess)

	var nilC *cooldown
	if p, _, ok, err := nilC.acquire(context.Background(), 0); !ok || err != nil {
		t.Fatal("nil breaker must be a no-op")
	} else {
		p.done(outcomeRateLimited)
	}
	checkNoLeak(t, before)
}

func TestBreakerEscalation(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newTestBreaker(8*time.Second, 30*time.Second, 35*time.Second)
	c.now = func() time.Time { return now }
	pauseLen := func() time.Duration { return c.until.Sub(now) }

	trip(c)
	if pauseLen() != 8*time.Second {
		t.Fatalf("first pause %s", pauseLen())
	}
	// stragglers sent before the trip must not extend or escalate
	trip(c)
	c.report(false, outcomeSuccess)
	if pauseLen() != 8*time.Second || c.penalty != 16*time.Second || !c.tripped {
		t.Fatalf("straggler changed state: pause=%s penalty=%s", pauseLen(), c.penalty)
	}
	for _, want := range []time.Duration{16 * time.Second, 30 * time.Second, 30 * time.Second} {
		c.report(true, outcomeRateLimited)
		if pauseLen() != want {
			t.Fatalf("pause %s, want %s", pauseLen(), want)
		}
	}
	c.report(true, outcomeSuccess)
	if c.tripped || c.penalty != 8*time.Second {
		t.Fatalf("probe success should close and reset: tripped=%v penalty=%s", c.tripped, c.penalty)
	}
	trip(c)
	if pauseLen() != 8*time.Second {
		t.Fatalf("pause after reset %s", pauseLen())
	}
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
	t.Logf("%d deletes, %d calls to baidu, %d throttled, %d probes, %s", n, f.calls.Load(), f.throttled.Load(), d.cooldown.probes, time.Since(start))
	if failed.Load() != 0 {
		t.Fatalf("%d deletes failed", failed.Load())
	}
	if f.throttled.Load() == 0 {
		t.Fatal("test did not exercise the window")
	}
	// only requests already in flight when the breaker opened, plus one probe
	// per pause, may reach Baidu during the window
	if max := int64(conc + d.cooldown.probes); f.throttled.Load() > max {
		t.Fatalf("throttled requests = %d, want <= %d (in-flight + probes)", f.throttled.Load(), max)
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
