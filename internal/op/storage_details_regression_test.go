package op

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
)

type detailsTestDriver struct {
	model.Storage
	noCache bool
	calls   atomic.Int64
	probe   func(context.Context) (*model.StorageDetails, error)
}

func (d *detailsTestDriver) Config() driver.Config {
	return driver.Config{Name: "DetailsRegression", NoCache: d.noCache}
}
func (d *detailsTestDriver) GetAddition() driver.Additional { return &struct{}{} }
func (d *detailsTestDriver) Init(context.Context) error     { return nil }
func (d *detailsTestDriver) Drop(context.Context) error     { return nil }
func (d *detailsTestDriver) List(context.Context, model.Obj, model.ListArgs) ([]model.Obj, error) {
	return nil, nil
}
func (d *detailsTestDriver) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	return nil, nil
}
func (d *detailsTestDriver) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	d.calls.Add(1)
	return d.probe(ctx)
}

var detailsTestSequence atomic.Uint64

func newDetailsTestDriver(t *testing.T, total int64) *detailsTestDriver {
	t.Helper()
	setDetailsTestSetting(t, conf.StorageDetailsCooldownSeconds, "0")
	setDetailsTestSetting(t, conf.StorageDetailsTimeoutSeconds, "15")
	d := &detailsTestDriver{Storage: model.Storage{
		MountPath: fmt.Sprintf("/details-test/%s/%d", t.Name(), detailsTestSequence.Add(1)),
		Status:    WORK, CacheExpiration: 30,
	}}
	d.probe = func(context.Context) (*model.StorageDetails, error) { return detailsTestValue(total), nil }
	t.Cleanup(func() { InvalidateStorageDetailsState(d.MountPath); Cache.InvalidateStorageDetails(d) })
	return d
}

func detailsTestValue(total int64) *model.StorageDetails {
	return &model.StorageDetails{DiskUsage: model.DiskUsage{TotalSpace: total}}
}

func setDetailsTestSetting(t *testing.T, key, value string) {
	t.Helper()
	previous, exists := Cache.GetSetting(key)
	Cache.SetSetting(key, &model.SettingItem{Key: key, Value: value})
	t.Cleanup(func() {
		if exists {
			Cache.SetSetting(key, previous)
		} else {
			Cache.settingCache.Delete(key)
		}
	})
}

type detailsTestResult struct {
	val *model.StorageDetails
	err error
}

func startDetailsTestCall(d driver.Driver, refresh bool) <-chan detailsTestResult {
	result := make(chan detailsTestResult, 1)
	go func() {
		defer close(result)
		val, err := GetStorageDetails(context.Background(), d, refresh)
		result <- detailsTestResult{val, err}
	}()
	return result
}

func awaitDetailsTestResult(t *testing.T, result <-chan detailsTestResult) detailsTestResult {
	t.Helper()
	select {
	case r := <-result:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("capacity query did not finish")
		return detailsTestResult{}
	}
}

func blockDetailsTestProbe(t *testing.T, d *detailsTestDriver, total int64) (<-chan struct{}, func()) {
	t.Helper()
	entered := make(chan struct{})
	exited := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		select {
		case <-entered:
		default:
			return
		}
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			t.Error("probe did not exit during cleanup")
		}
	})
	d.probe = func(ctx context.Context) (*model.StorageDetails, error) {
		defer close(exited)
		close(entered)
		select {
		case <-release:
			return detailsTestValue(total), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return entered, func() { once.Do(func() { close(release) }) }
}

func awaitDetailsTestEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("new probe joined the old flight")
	}
}

func TestInvalidateStorageDetailsStateDoesNotResurrectCache(t *testing.T) {
	setDetailsTestSetting(t, conf.StorageDetailsCooldownSeconds, "0")
	d := newDetailsTestDriver(t, 1000)
	entered, release := blockDetailsTestProbe(t, d, 1000)
	old := startDetailsTestCall(d, false)
	awaitDetailsTestEntered(t, entered)
	InvalidateStorageDetailsState(d.MountPath)
	release()
	if r := awaitDetailsTestResult(t, old); r.err != nil {
		t.Fatal(r.err)
	}
	if val, exists := Cache.GetStorageDetails(d); exists {
		t.Fatalf("invalidated generation resurrected capacity cache: %+v", val)
	}
}

func TestGetStorageDetailsSamePathReplacement(t *testing.T) {
	setDetailsTestSetting(t, conf.StorageDetailsCooldownSeconds, "0")
	oldDriver := newDetailsTestDriver(t, 1000)
	oldEntered, releaseOld := blockDetailsTestProbe(t, oldDriver, 1000)
	old := startDetailsTestCall(oldDriver, false)
	awaitDetailsTestEntered(t, oldEntered)
	InvalidateStorageDetailsState(oldDriver.MountPath)
	newDriver := newDetailsTestDriver(t, 2000)
	newDriver.MountPath = oldDriver.MountPath
	newEntered, releaseNew := blockDetailsTestProbe(t, newDriver, 2000)
	fresh := startDetailsTestCall(newDriver, false)
	awaitDetailsTestEntered(t, newEntered)
	releaseNew()
	if r := awaitDetailsTestResult(t, fresh); r.err != nil || r.val.TotalSpace != 2000 {
		t.Fatalf("fresh result: %+v", r)
	}
	releaseOld()
	awaitDetailsTestResult(t, old)
	if val, exists := Cache.GetStorageDetails(newDriver); !exists || val.TotalSpace != 2000 {
		t.Fatalf("old completion replaced new cache: %+v, %t", val, exists)
	}
}

func TestGetStorageDetailsBalanceIsolation(t *testing.T) {
	d := newDetailsTestDriver(t, 1000)
	partner := newDetailsTestDriver(t, 2000)
	partner.MountPath = d.MountPath + ".balance1"
	partner2 := newDetailsTestDriver(t, 3000)
	partner2.MountPath = d.MountPath + ".balance2"
	for _, candidate := range []*detailsTestDriver{d, partner, partner2} {
		if _, err := GetStorageDetails(context.Background(), candidate); err != nil {
			t.Fatal(err)
		}
	}
	if val, exists := Cache.GetStorageDetails(partner); !exists || val.TotalSpace != 2000 {
		t.Fatalf("balance partner shares main capacity: %+v, %t", val, exists)
	}
	detailsLock.Lock()
	before1, before2 := *detailsStates[partner.MountPath], *detailsStates[partner2.MountPath]
	detailsLock.Unlock()
	if _, err := GetStorageDetails(context.Background(), d, true); err != nil {
		t.Fatal(err)
	}
	detailsLock.Lock()
	after1, after2 := *detailsStates[partner.MountPath], *detailsStates[partner2.MountPath]
	detailsLock.Unlock()
	if before1 != after1 || before2 != after2 {
		t.Fatal("main refresh changed balance cooldown or generation")
	}
	InvalidateStorageDetailsState(d.MountPath)
	Cache.InvalidateStorageDetails(d)
	if val, exists := Cache.GetStorageDetails(partner); !exists || val.TotalSpace != 2000 {
		t.Fatalf("invalidating main removed partner capacity: %+v, %t", val, exists)
	}
	if val, exists := Cache.GetStorageDetails(partner2); !exists || val.TotalSpace != 3000 {
		t.Fatalf("invalidating main removed second partner capacity: %+v, %t", val, exists)
	}
}

func TestGetStorageDetailsBalanceConcurrent(t *testing.T) {
	main := newDetailsTestDriver(t, 1000)
	var results []<-chan singleflight.Result[*model.StorageDetails]
	var releases []func()
	for i := 0; i < 3; i++ {
		d := main
		if i > 0 {
			d = newDetailsTestDriver(t, int64((i+1)*1000))
			d.MountPath = main.MountPath + fmt.Sprintf(".balance%d", i)
		}
		entered, release := blockDetailsTestProbe(t, d, int64((i+1)*1000))
		results = append(results, admitDetailsTestCall(t, d, false, time.Second))
		releases = append(releases, release)
		awaitDetailsTestEntered(t, entered)
	}
	for _, release := range releases {
		release()
	}
	for i, result := range results {
		if r := awaitDetailsTestFlight(t, result); r.Err != nil || r.Val.TotalSpace != int64((i+1)*1000) {
			t.Fatalf("balance %d: %+v", i, r)
		}
	}
}

func admitDetailsTestCall(t *testing.T, d driver.Driver, refresh bool, timeout time.Duration) <-chan singleflight.Result[*model.StorageDetails] {
	t.Helper()
	result, cached, err := prepareStorageDetails(context.Background(), d, refresh, 0, timeout)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		resultChan := make(chan singleflight.Result[*model.StorageDetails], 1)
		resultChan <- singleflight.Result[*model.StorageDetails]{Val: cached}
		return resultChan
	}
	return result
}

func awaitDetailsTestFlight(t *testing.T, result <-chan singleflight.Result[*model.StorageDetails]) singleflight.Result[*model.StorageDetails] {
	t.Helper()
	select {
	case r := <-result:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("flight did not complete")
		return singleflight.Result[*model.StorageDetails]{}
	}
}

func newDetailsTestGate(t *testing.T) (chan struct{}, func()) {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	return release, open
}

func TestGetStorageDetailsNormalAfterRefresh(t *testing.T) {
	d := newDetailsTestDriver(t, 1000)
	oldEntered, freshEntered := make(chan struct{}), make(chan struct{})
	oldRelease, releaseOld := newDetailsTestGate(t)
	freshRelease, releaseFresh := newDetailsTestGate(t)
	var probes atomic.Int64
	d.probe = func(ctx context.Context) (*model.StorageDetails, error) {
		entered, release, total := oldEntered, oldRelease, int64(1000)
		if probes.Add(1) != 1 {
			entered, release, total = freshEntered, freshRelease, 2000
		}
		close(entered)
		select {
		case <-release:
			return detailsTestValue(total), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	old := admitDetailsTestCall(t, d, false, time.Second)
	awaitDetailsTestEntered(t, oldEntered)
	fresh := admitDetailsTestCall(t, d, true, time.Second)
	awaitDetailsTestEntered(t, freshEntered)
	follower := admitDetailsTestCall(t, d, false, time.Second)
	if calls := d.calls.Load(); calls != 2 {
		t.Fatalf("normal follower started an extra probe: %d", calls)
	}
	releaseFresh()
	for _, result := range []<-chan singleflight.Result[*model.StorageDetails]{fresh, follower} {
		if r := awaitDetailsTestFlight(t, result); r.Err != nil || r.Val.TotalSpace != 2000 {
			t.Fatalf("new generation result: %+v", r)
		}
	}
	releaseOld()
	awaitDetailsTestFlight(t, old)
	if val, exists := Cache.GetStorageDetails(d); !exists || val.TotalSpace != 2000 {
		t.Fatalf("stale completion changed cache: %+v", val)
	}
}

func TestGetStorageDetailsOverlappingRequests(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(fmt.Sprint(refresh), func(t *testing.T) {
			d := newDetailsTestDriver(t, 1000)
			entered, release := blockDetailsTestProbe(t, d, 1000)
			results := []<-chan singleflight.Result[*model.StorageDetails]{admitDetailsTestCall(t, d, refresh, time.Second)}
			awaitDetailsTestEntered(t, entered)
			for i := 1; i < 5; i++ {
				results = append(results, admitDetailsTestCall(t, d, refresh, time.Second))
			}
			if d.calls.Load() != 1 {
				t.Fatal("overlapping requests were not coalesced")
			}
			release()
			for _, result := range results {
				if r := awaitDetailsTestFlight(t, result); r.Err != nil || r.Val.TotalSpace != 1000 {
					t.Fatalf("result: %+v", r)
				}
			}
			val, err := GetStorageDetails(context.Background(), d)
			if err != nil || val.TotalSpace != 1000 || d.calls.Load() != 1 {
				t.Fatal("successful shared result was not cached")
			}
		})
	}
}

func TestGetStorageDetailsOldRefreshPreservesNewBatch(t *testing.T) {
	d := newDetailsTestDriver(t, 1000)
	oldEntered, freshEntered := make(chan struct{}), make(chan struct{})
	oldRelease, releaseOld := newDetailsTestGate(t)
	freshRelease, releaseFresh := newDetailsTestGate(t)
	var probes atomic.Int64
	d.probe = func(ctx context.Context) (*model.StorageDetails, error) {
		entered, release, total := oldEntered, oldRelease, int64(1000)
		if probes.Add(1) != 1 {
			entered, release, total = freshEntered, freshRelease, 2000
		}
		close(entered)
		select {
		case <-release:
			return detailsTestValue(total), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	old := admitDetailsTestCall(t, d, true, time.Second)
	awaitDetailsTestEntered(t, oldEntered)
	InvalidateStorageDetailsState(d.MountPath)
	fresh := admitDetailsTestCall(t, d, true, time.Second)
	awaitDetailsTestEntered(t, freshEntered)
	releaseOld()
	awaitDetailsTestFlight(t, old)
	follower := admitDetailsTestCall(t, d, true, time.Second)
	if d.calls.Load() != 2 {
		t.Fatal("old refresh cleared the new batch")
	}
	releaseFresh()
	awaitDetailsTestFlight(t, fresh)
	if r := awaitDetailsTestFlight(t, follower); r.Err != nil || r.Val.TotalSpace != 2000 {
		t.Fatalf("follower: %+v", r)
	}
}

func TestGetStorageDetailsRefreshRecoversAfterFailure(t *testing.T) {
	for _, failure := range []string{"error", "panic", "timeout", "late-success"} {
		t.Run(failure, func(t *testing.T) {
			d := newDetailsTestDriver(t, 2000)
			setDetailsTestSetting(t, conf.StorageDetailsCooldownSeconds, "60")
			var probes atomic.Int64
			d.probe = func(ctx context.Context) (*model.StorageDetails, error) {
				if probes.Add(1) == 1 {
					switch failure {
					case "error":
						return nil, errors.New("probe failed")
					case "panic":
						panic("probe failed")
					case "timeout":
						<-ctx.Done()
						return nil, ctx.Err()
					case "late-success":
						<-ctx.Done()
						return detailsTestValue(1000), nil
					}
				}
				return detailsTestValue(2000), nil
			}
			first, _, err := prepareStorageDetails(context.Background(), d, true, 60, 20*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if r := awaitDetailsTestFlight(t, first); r.Err == nil || r.Val != nil {
				t.Fatalf("failure published success: %+v", r)
			}
			if _, exists := Cache.GetStorageDetails(d); exists {
				t.Fatal("failed probe was cached")
			}
			val, err := GetStorageDetails(context.Background(), d, true)
			if err != nil || val.TotalSpace != 2000 || d.calls.Load() != 2 {
				t.Fatalf("retry: %v, %v", val, err)
			}
		})
	}
}

func TestGetStorageDetailsCallerCancellation(t *testing.T) {
	d := newDetailsTestDriver(t, 1000)
	entered := make(chan context.Context, 1)
	release, open := newDetailsTestGate(t)
	d.probe = func(ctx context.Context) (*model.StorageDetails, error) {
		entered <- ctx
		select {
		case <-release:
			return detailsTestValue(1000), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan detailsTestResult, 1)
	go func() { val, err := GetStorageDetails(ctx, d); first <- detailsTestResult{val, err} }()
	var probeCtx context.Context
	select {
	case probeCtx = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("probe not started")
	}
	follower := admitDetailsTestCall(t, d, false, time.Second)
	cancel()
	if r := awaitDetailsTestResult(t, first); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", r.err)
	}
	if probeCtx.Err() != nil {
		t.Fatal("caller cancellation cancelled shared probe")
	}
	open()
	if r := awaitDetailsTestFlight(t, follower); r.Err != nil || r.Val.TotalSpace != 1000 {
		t.Fatalf("other waiter: %+v", r)
	}
}

func TestGetStorageDetailsNoCache(t *testing.T) {
	d := newDetailsTestDriver(t, 1000)
	d.noCache = true
	Cache.SetStorageDetails(&detailsTestDriver{Storage: d.Storage}, detailsTestValue(999))
	for i := 0; i < 2; i++ {
		val, err := GetStorageDetails(context.Background(), d)
		if err != nil || val.TotalSpace != 1000 {
			t.Fatalf("NoCache read old cache: %+v, %v", val, err)
		}
	}
	if d.calls.Load() != 2 {
		t.Fatal("NoCache retained completed probe")
	}
}

func TestGetStorageDetailsExpiredCooldownPreservesGeneration(t *testing.T) {
	d := newDetailsTestDriver(t, 1000)
	detailsLock.Lock()
	state := storageDetailsStateLocked(d.MountPath)
	state.generation, state.lastDone, state.refreshing = 10, 1, true
	cleanExpiredLastDoneTimesLocked(time.Now().Unix())
	generation, refreshing, lastDone := state.generation, state.refreshing, state.lastDone
	detailsLock.Unlock()
	if generation != 10 || !refreshing || lastDone != 0 {
		t.Fatal("cooldown cleanup reset version or active refresh")
	}
}

type lifecycleDetailsTestDriver struct {
	*detailsTestDriver
	addition struct {
		Total int64 `json:"total"`
	}
	initHook func(context.Context) error
	dropHook func(context.Context) error
}

func (d *lifecycleDetailsTestDriver) GetAddition() driver.Additional { return &d.addition }
func (d *lifecycleDetailsTestDriver) Init(ctx context.Context) error {
	if d.initHook != nil {
		if err := d.initHook(ctx); err != nil {
			return err
		}
	}
	total := d.addition.Total
	d.probe = func(context.Context) (*model.StorageDetails, error) { return detailsTestValue(total), nil }
	return nil
}
func (d *lifecycleDetailsTestDriver) Drop(ctx context.Context) error {
	if d.dropHook != nil {
		return d.dropHook(ctx)
	}
	return nil
}

func newLifecycleDetailsTestDriver(t *testing.T) *lifecycleDetailsTestDriver {
	t.Helper()
	d := &lifecycleDetailsTestDriver{detailsTestDriver: newDetailsTestDriver(t, 1000)}
	d.Driver, d.Addition = "DetailsRegression", `{"total":1000}`
	if err := db.CreateStorage(&d.Storage); err != nil {
		t.Fatal(err)
	}
	oldPath := d.MountPath
	storagesMap.Store(oldPath, d)
	t.Cleanup(func() {
		storagesMap.Delete(oldPath)
		storagesMap.Delete(d.MountPath)
		if err := db.DeleteStorageById(d.ID); err != nil {
			t.Error(err)
		}
		detailsLock.Lock()
		delete(detailsChanges, d)
		detailsLock.Unlock()
	})
	return d
}

func TestGetStorageDetailsUpdateLifecycle(t *testing.T) {
	for _, rename := range []bool{false, true} {
		t.Run(fmt.Sprint(rename), func(t *testing.T) {
			d := newLifecycleDetailsTestDriver(t)
			oldPath := d.MountPath
			oldEntered, releaseOld := blockDetailsTestProbe(t, d.detailsTestDriver, 1000)
			old := admitDetailsTestCall(t, d, false, time.Second)
			awaitDetailsTestEntered(t, oldEntered)
			dropEntered, initEntered := make(chan struct{}), make(chan struct{})
			dropRelease, releaseDrop := newDetailsTestGate(t)
			initRelease, releaseInit := newDetailsTestGate(t)
			d.dropHook = func(context.Context) error { close(dropEntered); <-dropRelease; return nil }
			d.initHook = func(context.Context) error { close(initEntered); <-initRelease; return nil }
			updated := d.Storage
			updated.Addition = `{"total":2000}`
			if rename {
				updated.MountPath += "-renamed"
			}
			updateDone := make(chan error, 1)
			go func() { defer close(updateDone); updateDone <- UpdateStorage(context.Background(), updated) }()
			t.Cleanup(func() {
				releaseOld()
				releaseDrop()
				releaseInit()
				select {
				case <-updateDone:
				case <-time.After(3 * time.Second):
					t.Error("update did not exit during cleanup")
				}
			})
			awaitDetailsTestEntered(t, dropEntered)
			if _, err := GetStorageDetails(context.Background(), d); !errors.Is(err, errs.StorageNotInit) {
				t.Fatalf("probe admitted during Drop: %v", err)
			}
			releaseOld()
			awaitDetailsTestFlight(t, old)
			if _, exists := Cache.detailCache.Get(oldPath); exists {
				t.Fatal("old probe published during update")
			}
			releaseDrop()
			awaitDetailsTestEntered(t, initEntered)
			if _, err := GetStorageDetails(context.Background(), d, true); !errors.Is(err, errs.StorageNotInit) {
				t.Fatalf("probe admitted during Init: %v", err)
			}
			if d.calls.Load() != 1 {
				t.Fatal("intermediate driver was probed")
			}
			if !rename {
				rejected := updated
				rejected.Addition = `{"total":3000}`
				if err := UpdateStorage(context.Background(), rejected); !errors.Is(err, errs.StorageNotInit) {
					t.Fatalf("overlapping update was admitted: %v", err)
				}
				persisted, err := db.GetStorageById(updated.ID)
				if err != nil || persisted.Addition != updated.Addition {
					t.Fatal("rejected update changed persisted configuration")
				}
			}
			releaseInit()
			select {
			case err := <-updateDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("update did not finish")
			}
			val, err := GetStorageDetails(context.Background(), d)
			if err != nil || val.TotalSpace != 2000 {
				t.Fatalf("updated capacity: %+v, %v", val, err)
			}
			if rename {
				if _, exists := Cache.detailCache.Get(oldPath); exists {
					t.Fatal("renamed driver published to old path")
				}
			}
		})
	}
}

func TestGetStorageDetailsUpdateFailureAndRetry(t *testing.T) {
	for _, failure := range []string{"drop-error", "init-error", "init-panic"} {
		t.Run(failure, func(t *testing.T) {
			d := newLifecycleDetailsTestDriver(t)
			updated := d.Storage
			updated.Addition = `{"total":2000}`
			switch failure {
			case "drop-error":
				d.dropHook = func(context.Context) error { return errors.New("drop failed") }
			case "init-error":
				d.initHook = func(context.Context) error { return errors.New("init failed") }
			case "init-panic":
				d.initHook = func(context.Context) error { panic("init failed") }
			}
			if err := UpdateStorage(context.Background(), updated); err == nil {
				t.Fatal("failed update returned success")
			}
			if _, err := GetStorageDetails(context.Background(), d); !errors.Is(err, errs.StorageNotInit) {
				t.Fatalf("failed driver was queryable: %v", err)
			}
			d.dropHook, d.initHook = nil, nil
			if err := UpdateStorage(context.Background(), updated); err != nil {
				t.Fatal(err)
			}
			val, err := GetStorageDetails(context.Background(), d)
			if err != nil || val.TotalSpace != 2000 {
				t.Fatalf("retry capacity: %+v, %v", val, err)
			}
		})
	}
}

func TestGetStorageDetailsRetiredDriver(t *testing.T) {
	for _, action := range []string{"disable", "delete"} {
		t.Run(action, func(t *testing.T) {
			d := newLifecycleDetailsTestDriver(t)
			entered, release := blockDetailsTestProbe(t, d.detailsTestDriver, 1000)
			old := admitDetailsTestCall(t, d, false, time.Second)
			awaitDetailsTestEntered(t, entered)
			var err error
			if action == "disable" {
				err = DisableStorage(context.Background(), d.ID)
			} else {
				err = DeleteStorageById(context.Background(), d.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := GetStorageDetails(context.Background(), d); !errors.Is(err, errs.StorageNotInit) {
				t.Fatal("retired object was admitted")
			}
			release()
			awaitDetailsTestFlight(t, old)
			if _, exists := Cache.GetStorageDetails(d); exists {
				t.Fatal("retired object republished capacity")
			}
		})
	}
}
