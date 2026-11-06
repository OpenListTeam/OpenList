package op

import (
	"context"
	"errors"
	"fmt"
	"path"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// detailsDriver is a WithDetails driver whose probe can be swapped and blocked.
type detailsDriver struct {
	model.Storage
	addition struct {
		Total int64 `json:"total"`
	}
	noCache     bool
	checkStatus bool
	calls       atomic.Int64
	active      atomic.Int64
	maxActive   atomic.Int64
	initCalls   atomic.Int64
	dropCalls   atomic.Int64
	mu          sync.Mutex
	probe       func(context.Context) (*model.StorageDetails, error)
}

func (d *detailsDriver) Config() driver.Config {
	return driver.Config{Name: "DetailsTest", NoCache: d.noCache, CheckStatus: d.checkStatus}
}
func (d *detailsDriver) GetAddition() driver.Additional { return &d.addition }
func (d *detailsDriver) Init(context.Context) error {
	d.initCalls.Add(1)
	total := d.addition.Total
	d.setProbe(func(context.Context) (*model.StorageDetails, error) { return capacity(total), nil })
	return nil
}
func (d *detailsDriver) Drop(context.Context) error { d.dropCalls.Add(1); return nil }
func (d *detailsDriver) List(context.Context, model.Obj, model.ListArgs) ([]model.Obj, error) {
	return nil, nil
}
func (d *detailsDriver) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	return nil, nil
}
func (d *detailsDriver) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	d.calls.Add(1)
	active := d.active.Add(1)
	defer d.active.Add(-1)
	for previous := d.maxActive.Load(); active > previous; previous = d.maxActive.Load() {
		if d.maxActive.CompareAndSwap(previous, active) {
			break
		}
	}
	d.mu.Lock()
	probe := d.probe
	d.mu.Unlock()
	return probe(ctx)
}
func (d *detailsDriver) setProbe(probe func(context.Context) (*model.StorageDetails, error)) {
	d.mu.Lock()
	d.probe = probe
	d.mu.Unlock()
}
func (d *detailsDriver) setTotal(total int64) {
	d.setProbe(func(context.Context) (*model.StorageDetails, error) { return capacity(total), nil })
}

// block makes the next probes wait for release and reports when one starts.
func (d *detailsDriver) block(total int64) (started <-chan struct{}, release func()) {
	s, r := make(chan struct{}, 16), make(chan struct{})
	var once sync.Once
	d.setProbe(func(context.Context) (*model.StorageDetails, error) {
		s <- struct{}{}
		<-r
		return capacity(total), nil
	})
	return s, func() { once.Do(func() { close(r) }) }
}

func capacity(total int64) *model.StorageDetails {
	return &model.StorageDetails{DiskUsage: model.DiskUsage{TotalSpace: total}}
}

var detailsTestSeq atomic.Int64

// newDetailsDriver registers a working driver at a fresh mount path.
func newDetailsDriver(t *testing.T, total int64) *detailsDriver {
	t.Helper()
	setDetailsSetting(t, conf.StorageDetailsTimeoutSeconds, "15")
	d := &detailsDriver{Storage: model.Storage{
		MountPath:       fmt.Sprintf("/details/%d", detailsTestSeq.Add(1)),
		Driver:          "DetailsTest",
		Addition:        fmt.Sprintf(`{"total":%d}`, total),
		Status:          WORK,
		CacheExpiration: 30,
	}}
	d.addition.Total = total
	d.setTotal(total)
	storagesMap.Store(d.MountPath, d)
	t.Cleanup(func() {
		detailsLock.Lock()
		var tasks []*detailsTask
		for _, task := range detailsTasks {
			if task.storage == d {
				tasks = append(tasks, task)
			}
		}
		detailsLock.Unlock()
		for _, task := range tasks {
			waitStarted(t, task.done)
		}
		storagesMap.Delete(d.MountPath)
		beginStorageDetailsChange(d.MountPath)()
	})
	return d
}

func setDetailsSetting(t *testing.T, key, value string) {
	t.Helper()
	previous, existed := Cache.GetSetting(key)
	Cache.SetSetting(key, &model.SettingItem{Key: key, Value: value})
	t.Cleanup(func() {
		if existed {
			Cache.SetSetting(key, previous)
		} else {
			Cache.settingCache.Delete(key)
		}
	})
}

func mustTotal(t *testing.T, d driver.Driver, refresh bool, want int64) {
	t.Helper()
	got, err := GetStorageDetails(context.Background(), d, refresh)
	if err != nil || got == nil || got.TotalSpace != want {
		t.Fatalf("want %d, got %+v, %v", want, got, err)
	}
}

func waitStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not start")
	}
}

func TestStorageDetailsCachedAndShared(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	started, release := d.block(1000)
	defer release()
	var tasks []*detailsTask
	for range 8 {
		task, _, err := prepareStorageDetails(context.Background(), d, false, 15*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	waitStarted(t, started)
	release()
	for _, task := range tasks {
		if task != tasks[0] {
			t.Fatal("ordinary requests did not share one task")
		}
		if value, err := task.wait(context.Background()); err != nil || value == nil || value.TotalSpace != 1000 {
			t.Fatalf("shared task: %+v, %v", value, err)
		}
	}
	mustTotal(t, d, false, 1000)
	if n := d.calls.Load(); n != 1 {
		t.Fatalf("concurrent and cached requests probed %d times", n)
	}
}

func TestStorageDetailsSharedProbeSurvivesCallerCancel(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	started, release := d.block(1000)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := GetStorageDetails(ctx, d); first <- err }()
	waitStarted(t, started)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: %v", err)
	}
	second := make(chan error, 1)
	go func() { _, err := GetStorageDetails(context.Background(), d); second <- err }()
	release()
	if err := <-second; err != nil {
		t.Fatalf("joined caller failed after first caller cancelled: %v", err)
	}
	mustTotal(t, d, false, 1000)
	if n := d.calls.Load(); n != 1 {
		t.Fatalf("probed %d times", n)
	}
}

func TestStorageDetailsRefreshAndIgnoresLegacyCooldown(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	mustTotal(t, d, false, 1000)
	d.setTotal(2000)
	mustTotal(t, d, false, 1000) // cached
	mustTotal(t, d, true, 2000)  // refresh bypasses cache
	mustTotal(t, d, false, 2000) // and updates it

	setDetailsSetting(t, "storage_details_cooldown_seconds", "60")
	d.setTotal(3000)
	calls := d.calls.Load()
	mustTotal(t, d, true, 3000) // legacy setting must not suppress force refresh
	if d.calls.Load() != calls+1 {
		t.Fatal("legacy cooldown changed task admission")
	}
}

func TestStorageDetailsFailedProbeIsNotCached(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	d.setProbe(func(context.Context) (*model.StorageDetails, error) { return nil, errors.New("boom") })
	if _, err := GetStorageDetails(context.Background(), d); err == nil {
		t.Fatal("expected probe error")
	}
	// Failed probe must NOT pollute detailCache with nil, but marks detailsState.failed:
	// ordinary refresh returns (nil, StorageNotInit) without re-fetching.
	if _, exists := Cache.GetStorageDetails(d); exists {
		t.Fatal("failed probe polluted detailCache")
	}
	callsAfterFail := d.calls.Load()
	if val, err := GetStorageDetails(context.Background(), d, false); !errors.Is(err, errs.StorageNotInit) || val != nil {
		t.Fatalf("ordinary refresh on failed state should return (nil, StorageNotInit), got (%+v, %v)", val, err)
	}
	if d.calls.Load() != callsAfterFail {
		t.Fatal("ordinary refresh re-fetched a failed mount without force refresh")
	}
	// Force refresh clears the failed-empty entry and triggers a new probe.
	d.setProbe(func(context.Context) (*model.StorageDetails, error) { panic("driver bug") })
	if _, err := GetStorageDetails(context.Background(), d, true); err == nil {
		t.Fatal("expected panic to become an error on forced retry")
	}
	d.setProbe(func(context.Context) (*model.StorageDetails, error) { return nil, nil })
	if val, err := GetStorageDetails(context.Background(), d, true); err != nil || val != nil {
		t.Fatalf("nil capacity: %+v, %v", val, err)
	}
	d.setTotal(1000)
	mustTotal(t, d, true, 1000)
	mustTotal(t, d, false, 1000)

	// Simulate an initially working account (cached 1000) that later becomes inaccessible:
	// once refreshed (or after TTL expiry), its capacity becomes failed-empty nil ("-"),
	// and subsequent ordinary refreshes return "-" immediately without hitting the remote driver.
	d.setProbe(func(context.Context) (*model.StorageDetails, error) {
		return nil, errors.New("AADSTS70000: grant is expired")
	})
	files := GetStorageVirtualFilesWithDetailsByPath(context.Background(), path.Dir(d.MountPath), false, true, path.Base(d.MountPath))
	if len(files) != 1 || files[0].(*model.ObjStorageDetails).StorageDetails != nil {
		t.Fatalf("unavailable account should degrade capacity to nil (-), got %+v", files)
	}
	callsBeforeOrdinary := d.calls.Load()
	files = GetStorageVirtualFilesWithDetailsByPath(context.Background(), path.Dir(d.MountPath), false, false, path.Base(d.MountPath))
	if len(files) != 1 || files[0].(*model.ObjStorageDetails).StorageDetails != nil {
		t.Fatalf("ordinary refresh after failure should still show (-), got %+v", files)
	}
	if d.calls.Load() != callsBeforeOrdinary {
		t.Fatal("ordinary refresh after failure triggered another remote probe")
	}
	// When admin fixes the storage config (triggers lifecycle change), ordinary refresh (refresh=false)
	// must automatically fetch without requiring force refresh.
	d.setTotal(2000)
	beginStorageDetailsChange(d.MountPath)()
	mustTotal(t, d, false, 2000)
}

func TestStorageDetailsStaleProbeIsDiscardedAfterChange(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	started, release := d.block(1000)
	defer release()
	oldTask, _, err := prepareStorageDetails(context.Background(), d, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	old := make(chan *model.StorageDetails, 1)
	go func() { v, _ := oldTask.wait(context.Background()); old <- v }()
	waitStarted(t, started)

	end := beginStorageDetailsChange(d.MountPath)
	if _, err := GetStorageDetails(context.Background(), d); !errors.Is(err, errs.StorageNotInit) {
		t.Fatalf("probe admitted during change: %v", err)
	}
	d.setTotal(2000) // the "reinitialized" driver
	end()
	// Even while the stale probe from the previous generation remains blocked,
	// the reinitialized mount can start a new probe in the new generation.
	mustTotal(t, d, false, 2000)
	release()
	waitStarted(t, oldTask.done)
	if val := <-old; val != nil {
		t.Fatalf("stale result returned: %+v", val)
	}
	mustTotal(t, d, false, 2000)
}

// Regression: forcing an in-flight ordinary probe must clear old cache now,
// while joining that same remote call rather than starting another one.
func TestStorageDetailsForceInvalidationRegression(t *testing.T) {
	d := newDetailsDriver(t, 2000)
	started, release := d.block(2000)
	defer release()
	first, _, err := prepareStorageDetails(context.Background(), d, false, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, started)
	Cache.detailCache.Set(d.MountPath, capacity(1000))
	forced, _, err := prepareStorageDetails(context.Background(), d, true, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := Cache.detailCache.Get(d.MountPath); exists {
		t.Error("force refresh left the old capacity cache readable")
	}
	if first != forced {
		t.Fatal("force refresh replaced an active ordinary task")
	}
	normal, cached, err := prepareStorageDetails(context.Background(), d, false, 15*time.Second)
	if err != nil || cached != nil || normal != first {
		t.Fatalf("ordinary request after force: %p, %+v, %v", normal, cached, err)
	}
	release()
	waitStarted(t, first.done)
	if d.calls.Load() != 1 {
		t.Errorf("force refresh did not reuse the ordinary probe: %d calls", d.calls.Load())
	}
	mustTotal(t, d, false, 2000)
}

// A deadline must release waiters, not the slot of a still-running driver.
func TestStorageDetailsTimedOutSlotRegression(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	started, release := d.block(1000)
	defer release()
	first, _, err := prepareStorageDetails(context.Background(), d, false, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, started)
	if _, err := first.wait(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	for _, force := range []bool{false, true, false, true} {
		next, _, err := prepareStorageDetails(context.Background(), d, force, 20*time.Millisecond)
		if err != nil || next != first {
			t.Fatalf("expired slot was replaced: %p, %v", next, err)
		}
		if _, err := next.wait(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected occupied expired task, got %v", err)
		}
	}
	if d.calls.Load() != 1 {
		t.Errorf("timed-out driver was still running but a second call started: %d", d.calls.Load())
	}
	release()
	waitStarted(t, first.done)
	if _, exists := Cache.detailCache.Get(d.MountPath); exists {
		t.Fatal("late success after deadline was cached")
	}
	d.setTotal(2000)
	mustTotal(t, d, true, 2000)
	if d.maxActive.Load() != 1 {
		t.Fatalf("overlapping actual calls: %d", d.maxActive.Load())
	}
}

func TestStorageDetailsRetiredDriverIsRejected(t *testing.T) {
	if _, err := GetStorageDetails(context.Background(), nil); !errors.Is(err, errs.StorageNotInit) {
		t.Fatalf("nil driver: %v", err)
	}
	d := newDetailsDriver(t, 1000)
	replacement := &detailsDriver{Storage: d.Storage}
	replacement.setTotal(2000)
	storagesMap.Store(d.MountPath, replacement)
	if _, err := GetStorageDetails(context.Background(), d); !errors.Is(err, errs.StorageNotInit) {
		t.Fatalf("replaced driver: %v", err)
	}
	mustTotal(t, replacement, false, 2000)
	storagesMap.Delete(d.MountPath)
	if _, err := GetStorageDetails(context.Background(), replacement); !errors.Is(err, errs.StorageNotInit) {
		t.Fatalf("unmounted driver: %v", err)
	}
}

func TestStorageDetailsSettingsAreClamped(t *testing.T) {
	for _, c := range []struct {
		timeout     string
		wantTimeout time.Duration
	}{
		{"0", defaultDetailsTimeoutSec * time.Second},
		{"-1", defaultDetailsTimeoutSec * time.Second},
		{"999999", maxDetailsTimeoutSec * time.Second},
		{"bad", defaultDetailsTimeoutSec * time.Second},
		{"2", 2 * time.Second},
	} {
		setDetailsSetting(t, conf.StorageDetailsTimeoutSeconds, c.timeout)
		if timeout := storageDetailsSettings(); timeout != c.wantTimeout {
			t.Fatalf("%s: got %s", c.timeout, timeout)
		}
	}
}

func TestStorageDetailsCacheHitDoesNotFetch(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	Cache.detailCache.Set(d.MountPath, capacity(500))
	mustTotal(t, d, false, 500)
	if d.calls.Load() != 0 {
		t.Fatal("cache hit started a remote call")
	}
}

func TestStorageDetailsMixedAdmission(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	started, release := d.block(1000)
	defer release()
	const count = 32
	tasks := make(chan *detailsTask, count)
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, _, err := prepareStorageDetails(context.Background(), d, i%2 == 0, time.Second)
			tasks <- task
			errors <- err
		}()
	}
	wg.Wait()
	waitStarted(t, started)
	first := <-tasks
	for range count - 1 {
		if task := <-tasks; task != first {
			t.Fatal("mixed requests created different tasks")
		}
	}
	for range count {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	release()
	waitStarted(t, first.done)
	if d.calls.Load() != 1 || d.maxActive.Load() != 1 {
		t.Fatalf("calls=%d, maximum active=%d", d.calls.Load(), d.maxActive.Load())
	}
}

func TestStorageDetailsPublicationAndSlotAreAtomic(t *testing.T) {
	d := newDetailsDriver(t, 2000)
	returned, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	d.setProbe(func(context.Context) (*model.StorageDetails, error) {
		<-release
		close(returned)
		return capacity(2000), nil
	})
	task, _, err := prepareStorageDetails(context.Background(), d, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		detailsLock.Lock()
		defer detailsLock.Unlock()
		unblock()
		waitStarted(t, returned) // driver returned; publication is blocked by us
		if detailsTasks[d.MountPath] != task {
			t.Error("slot was released before publication")
		}
		if _, exists := Cache.detailCache.Get(d.MountPath); exists {
			t.Error("result was published outside the completion lock")
		}
		select {
		case <-task.done:
			t.Error("completion notified before publication")
		default:
		}
	}()
	waitStarted(t, task.done)
	detailsLock.Lock()
	cached, exists := Cache.detailCache.Get(d.MountPath)
	finished := detailsTasks[d.MountPath] == nil
	detailsLock.Unlock()
	if !finished || !exists || cached.TotalSpace != 2000 {
		t.Fatalf("inconsistent completion: slot free=%t, cache=%+v", finished, cached)
	}
	started, finish := d.block(3000)
	defer finish()
	next, _, err := prepareStorageDetails(context.Background(), d, true, time.Second)
	if err != nil || next == task {
		t.Fatalf("force after completion did not create next task: %p, %v", next, err)
	}
	waitStarted(t, started)
	if _, exists := Cache.detailCache.Get(d.MountPath); exists {
		t.Error("force after completion did not delete the new cache")
	}
	finish()
	waitStarted(t, next.done)
	mustTotal(t, d, false, 3000)
}

func TestStorageDetailsCanceledAdmissionAndCompletedResult(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GetStorageDetails(ctx, d, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled: %v", err)
	}
	if d.calls.Load() != 0 {
		t.Fatal("already canceled request created a task")
	}
	task, _, err := prepareStorageDetails(context.Background(), d, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, task.done)
	if val, err := task.wait(ctx); err != nil || val == nil || val.TotalSpace != 1000 {
		t.Fatalf("simultaneously ready completion was lost: %+v, %v", val, err)
	}
}

func TestStorageDetailsDeadlineWhilePublicationIsBlocked(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	returned := make(chan struct{})
	started, release := d.block(1000)
	defer release()
	task, _, err := prepareStorageDetails(context.Background(), d, false, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, started)
	func() {
		detailsLock.Lock()
		defer detailsLock.Unlock()
		release()
		go func() { <-task.ctx.Done(); close(returned) }()
		waitStarted(t, returned)
	}()
	waitStarted(t, task.done)
	if value, err := task.wait(context.Background()); value != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late publication was accepted: %+v, %v", value, err)
	}
	if _, exists := Cache.detailCache.Get(d.MountPath); exists {
		t.Fatal("success was cached after the task deadline")
	}
}

func TestStorageDetailsHiddenListDoesNotFetch(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	files := GetStorageVirtualFilesWithDetailsByPath(context.Background(), path.Dir(d.MountPath), true, true, path.Base(d.MountPath))
	if len(files) != 1 || d.calls.Load() != 0 {
		t.Fatal("hidden capacity triggered a remote task or changed the directory result")
	}
}

func TestStorageDetailsListBudgetAndBackgroundCompletion(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	Cache.SetStorageDetails(d, capacity(500))
	seen := make(chan context.Context, 1)
	release := make(chan struct{})
	d.setProbe(func(ctx context.Context) (*model.StorageDetails, error) {
		seen <- ctx
		select {
		case <-release:
			return capacity(1000), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	files := GetStorageVirtualFilesWithDetailsByPath(ctx, path.Dir(d.MountPath), false, true, path.Base(d.MountPath))
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond || elapsed > 1800*time.Millisecond {
		t.Fatalf("one-second capacity budget: %s", elapsed)
	}
	if len(files) != 1 || files[0].(*model.ObjStorageDetails).StorageDetails != nil {
		t.Fatalf("slow capacity must not block the directory response: %+v", files)
	}
	probeCtx := <-seen
	cancel()
	if err := probeCtx.Err(); err != nil {
		t.Fatalf("page completion canceled the shared task: %v", err)
	}
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer shortCancel()
	if val, err := GetStorageDetails(shortCtx, d, false); val != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ordinary refresh before background completion = (%v, %v), want (nil, DeadlineExceeded)", val, err)
	}
	detailsLock.Lock()
	task := detailsTasks[d.MountPath]
	detailsLock.Unlock()
	if task == nil {
		t.Fatal("background task disappeared after one second")
	}
	close(release)
	waitStarted(t, task.done)
	mustTotal(t, d, false, 1000)
	if d.calls.Load() != 1 {
		t.Fatal("next page did not use the background result")
	}
}

func TestStorageDetailsNoCacheStillReturnsSharedResult(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	d.noCache = true
	Cache.detailCache.Set(d.MountPath, capacity(500))
	started, release := d.block(1000)
	defer release()
	first, cached, err := prepareStorageDetails(context.Background(), d, false, time.Second)
	if err != nil || cached != nil || first == nil {
		t.Fatalf("NoCache read persistent cache: %+v, %v", cached, err)
	}
	waitStarted(t, started)
	second, _, err := prepareStorageDetails(context.Background(), d, true, time.Second)
	if err != nil || second != first {
		t.Fatalf("NoCache did not share running task: %v", err)
	}
	release()
	waitStarted(t, first.done)
	if val, err := first.wait(context.Background()); err != nil || val == nil || val.TotalSpace != 1000 {
		t.Fatalf("NoCache lost completed result: %+v, %v", val, err)
	}
	if _, exists := Cache.detailCache.Get(d.MountPath); exists {
		t.Fatal("NoCache result was persisted")
	}
	d.setTotal(2000)
	mustTotal(t, d, false, 2000)
}

func TestStorageDetailsFullMountIdentity(t *testing.T) {
	d := newDetailsDriver(t, 1000)
	balanced := newDetailsDriver(t, 2000)
	storagesMap.Delete(balanced.MountPath)
	balanced.MountPath = d.MountPath + ".balance"
	storagesMap.Store(balanced.MountPath, balanced)
	started, release := d.block(1000)
	defer release()
	task, _, err := prepareStorageDetails(context.Background(), d, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, started)
	mustTotal(t, balanced, false, 2000) // another mount must not wait for d
	release()
	waitStarted(t, task.done)
	mustTotal(t, d, false, 1000)
	mustTotal(t, balanced, false, 2000)
}

func TestStorageDetailsNeverChangesStorageState(t *testing.T) {
	for _, outcome := range []string{"success", "error", "panic", "timeout", "check-status"} {
		t.Run(outcome, func(t *testing.T) {
			d := newDetailsDriver(t, 1000)
			d.Status = "driver-specific-state"
			d.Disabled = true
			d.Remark = "untouched"
			d.checkStatus = outcome == "check-status"
			switch outcome {
			case "error":
				d.setProbe(func(context.Context) (*model.StorageDetails, error) { return nil, errors.New("quota unavailable") })
			case "panic":
				d.setProbe(func(context.Context) (*model.StorageDetails, error) { panic("quota panic") })
			case "timeout":
				d.setProbe(func(ctx context.Context) (*model.StorageDetails, error) { <-ctx.Done(); return capacity(1000), nil })
			}
			before := d.Storage
			task, _, err := prepareStorageDetails(context.Background(), d, true, 10*time.Millisecond)
			if outcome == "check-status" {
				if !errors.Is(err, errs.StorageNotInit) || d.calls.Load() != 0 {
					t.Fatalf("driver CheckStatus was not respected: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				_, _ = task.wait(context.Background())
				waitStarted(t, task.done)
			}
			if !reflect.DeepEqual(before, d.Storage) {
				t.Fatal("optional capacity query changed mount metadata or status")
			}
			if d.initCalls.Load() != 0 || d.dropCalls.Load() != 0 {
				t.Fatal("optional capacity query initialized or dropped the mount")
			}
		})
	}
}

// Lifecycle functions must retire capacity probed before the change.
func TestStorageDetailsLifecycle(t *testing.T) {
	// Also allow this focused suite to run without importing every driver
	// through op_test; the ordinary package suite already initializes this DB.
	if db.GetDb() == nil {
		testDB, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		conf.Conf = conf.DefaultConfig("data")
		db.Init(testDB)
	}
	for _, rename := range []bool{false, true} {
		t.Run(fmt.Sprint("rename=", rename), func(t *testing.T) {
			d := newDetailsDriver(t, 1000)
			if err := db.CreateStorage(&d.Storage); err != nil {
				t.Fatal(err)
			}
			oldPath := d.MountPath
			t.Cleanup(func() {
				storagesMap.Delete(d.MountPath)
				_ = db.DeleteStorageById(d.ID)
			})
			mustTotal(t, d, false, 1000)
			started, release := d.block(1000)
			defer release()
			oldTask, _, err := prepareStorageDetails(context.Background(), d, true, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			old := make(chan struct{})
			go func() { defer close(old); _, _ = oldTask.wait(context.Background()) }()
			waitStarted(t, started)

			updated := d.Storage
			updated.Addition = `{"total":2000}`
			if rename {
				updated.MountPath += "-renamed"
			}
			if err := UpdateStorage(context.Background(), updated); err != nil {
				t.Fatal(err)
			}
			mustTotal(t, d, false, 2000)
			release()
			waitStarted(t, oldTask.done)
			<-old
			mustTotal(t, d, false, 2000)
			if rename {
				if _, ok := Cache.detailCache.Get(oldPath); ok {
					t.Fatal("old mount path kept capacity")
				}
			}

			if err := DisableStorage(context.Background(), d.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := GetStorageDetails(context.Background(), d); !errors.Is(err, errs.StorageNotInit) {
				t.Fatalf("disabled driver: %v", err)
			}
			if _, ok := Cache.detailCache.Get(d.MountPath); ok {
				t.Fatal("disabled storage kept capacity")
			}
		})
	}
	for _, action := range []struct {
		name string
		run  func(context.Context, uint) error
	}{{"disable-active", DisableStorage}, {"delete-active", DeleteStorageById}} {
		t.Run(action.name, func(t *testing.T) {
			d := newDetailsDriver(t, 1000)
			if err := db.CreateStorage(&d.Storage); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.DeleteStorageById(d.ID) })
			started, release := d.block(1000)
			defer release()
			task, _, err := prepareStorageDetails(context.Background(), d, false, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			waitStarted(t, started)
			Cache.detailCache.Set(d.MountPath, capacity(500))
			if err := action.run(context.Background(), d.ID); err != nil {
				t.Fatal(err)
			}
			replacement := &detailsDriver{Storage: d.Storage}
			replacement.setTotal(2000)
			storagesMap.Store(d.MountPath, replacement)
			// While the old driver's GetDetails remains blocked, a replacement
			// driver mounted at the same path must be able to probe and cache
			// its own capacity independently.
			mustTotal(t, replacement, false, 2000)
			release()
			waitStarted(t, task.done)
			if val, _ := task.wait(context.Background()); val != nil {
				t.Fatal("removed driver's result escaped")
			}
			mustTotal(t, replacement, false, 2000)
			if replacement.calls.Load() != 1 {
				t.Fatalf("retired task overwrote replacement cache; calls=%d", replacement.calls.Load())
			}
		})
	}
}
