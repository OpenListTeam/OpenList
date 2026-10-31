package op_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"gorm.io/gorm"
)

func setMockDetailsCooldown(t *testing.T, value string) {
	t.Helper()
	previous, err := db.GetSettingItemByKey(conf.StorageDetailsCooldownSeconds)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal(err)
	}
	if err := op.SaveSettingItem(&model.SettingItem{
		Key: conf.StorageDetailsCooldownSeconds, Value: value, Type: conf.TypeNumber, Group: model.STYLE,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if previous != nil {
			if err := op.SaveSettingItem(previous); err != nil {
				t.Error(err)
			}
		} else {
			if err := db.DeleteSettingItemByKey(conf.StorageDetailsCooldownSeconds); err != nil {
				t.Error(err)
			}
			op.SettingCacheUpdate()
		}
	})
}

type mockDriverWithDetails struct {
	model.Storage
	callCount    int64
	delay        time.Duration
	firstEntered chan struct{}
	releaseFirst chan struct{}
}

func (m *mockDriverWithDetails) Config() driver.Config {
	return driver.Config{Name: "MockDetails"}
}

func (m *mockDriverWithDetails) GetAddition() driver.Additional {
	return nil
}

func (m *mockDriverWithDetails) Init(ctx context.Context) error {
	return nil
}

func (m *mockDriverWithDetails) Drop(ctx context.Context) error {
	return nil
}

func (m *mockDriverWithDetails) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return nil, nil
}

func (m *mockDriverWithDetails) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return nil, nil
}

func (m *mockDriverWithDetails) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	count := atomic.AddInt64(&m.callCount, 1)
	if count == 1 && m.firstEntered != nil {
		close(m.firstEntered)
		select {
		case <-m.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: 1000,
			UsedSpace:  200,
		},
	}, nil
}

func TestGetStorageDetailsSingleflight(t *testing.T) {
	mock := &mockDriverWithDetails{
		Storage: model.Storage{
			MountPath:       "/test-mock-singleflight",
			Status:          op.WORK,
			CacheExpiration: 30,
		},
		delay: 50 * time.Millisecond,
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = op.GetStorageDetails(ctx, mock, true)
		}()
	}
	wg.Wait()

	// Concurrent calls should be coalesced by singleflight into 1 execution
	if count := atomic.LoadInt64(&mock.callCount); count != 1 {
		t.Errorf("expected singleflight callCount 1, got %d", count)
	}
}

func TestGetStorageDetailsInvalidateOnRefresh(t *testing.T) {
	mock := &mockDriverWithDetails{
		Storage: model.Storage{
			MountPath:       "/test-mock-invalidate-refresh",
			Status:          op.WORK,
			CacheExpiration: 30,
		},
	}

	// Default cooldown is 0
	setMockDetailsCooldown(t, "0")

	ctx := context.Background()

	// 1. Initial fetch
	d1, err := op.GetStorageDetails(ctx, mock, false)
	if err != nil || d1.TotalSpace != 1000 {
		t.Fatalf("first call failed: %v", err)
	}
	if count := atomic.LoadInt64(&mock.callCount); count != 1 {
		t.Fatalf("expected callCount 1, got %d", count)
	}

	// 2. Normal read should hit cache (callCount remains 1)
	d2, err := op.GetStorageDetails(ctx, mock, false)
	if err != nil || d2.TotalSpace != 1000 {
		t.Fatalf("second call failed: %v", err)
	}
	if count := atomic.LoadInt64(&mock.callCount); count != 1 {
		t.Errorf("expected callCount still 1 on cached read, got %d", count)
	}

	// 3. Force refresh (refresh=true) with cooldown=0 should invalidate cache and query driver again
	d3, err := op.GetStorageDetails(ctx, mock, true)
	if err != nil || d3.TotalSpace != 1000 {
		t.Fatalf("third call failed: %v", err)
	}
	if count := atomic.LoadInt64(&mock.callCount); count != 2 {
		t.Errorf("expected callCount 2 on forced refresh, got %d", count)
	}
}

func TestGetStorageDetailsCooldown(t *testing.T) {
	mock := &mockDriverWithDetails{
		Storage: model.Storage{
			MountPath:       "/test-mock-cooldown-configured",
			Status:          op.WORK,
			CacheExpiration: 30,
		},
	}

	// Set cooldown to 3 seconds for test
	setMockDetailsCooldown(t, "3")

	ctx := context.Background()

	d1, err := op.GetStorageDetails(ctx, mock, true)
	if err != nil || d1.TotalSpace != 1000 {
		t.Fatalf("first call failed: %v", err)
	}
	if count := atomic.LoadInt64(&mock.callCount); count != 1 {
		t.Fatalf("expected callCount 1, got %d", count)
	}

	// Immediate second call should be protected by 3s cooldown
	d2, err := op.GetStorageDetails(ctx, mock, true)
	if err != nil || d2.TotalSpace != 1000 {
		t.Fatalf("second call failed: %v", err)
	}
	if count := atomic.LoadInt64(&mock.callCount); count != 1 {
		t.Errorf("expected callCount still 1 during cooldown, got %d", count)
	}
}

type mockPanicDriverWithDetails struct {
	model.Storage
}

func (m *mockPanicDriverWithDetails) Config() driver.Config {
	return driver.Config{Name: "MockPanicDetails"}
}

func (m *mockPanicDriverWithDetails) GetAddition() driver.Additional {
	return nil
}

func (m *mockPanicDriverWithDetails) Init(ctx context.Context) error {
	return nil
}

func (m *mockPanicDriverWithDetails) Drop(ctx context.Context) error {
	return nil
}

func (m *mockPanicDriverWithDetails) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return nil, nil
}

func (m *mockPanicDriverWithDetails) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return nil, nil
}

func (m *mockPanicDriverWithDetails) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	panic("unexpected driver sdk crash")
}

func TestGetStorageDetailsPanicRecovery(t *testing.T) {
	mock := &mockPanicDriverWithDetails{
		Storage: model.Storage{
			MountPath:       "/test-mock-panic",
			Status:          op.WORK,
			CacheExpiration: 30,
		},
	}

	ctx := context.Background()
	// Must not crash the process when driver panics
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic in test caller: %v", r)
		}
	}()

	details, err := op.GetStorageDetails(ctx, mock, true)
	if err == nil {
		t.Fatalf("expected error from panic in singleflight, got details: %v", details)
	}
}

func TestInvalidateStorageDetailsState(t *testing.T) {
	mock := &mockDriverWithDetails{
		Storage: model.Storage{
			MountPath:       "/test-mock-invalidate-state",
			Status:          op.WORK,
			CacheExpiration: 30,
		},
	}

	setMockDetailsCooldown(t, "60")

	ctx := context.Background()

	// 1. First fetch establishes cooldown
	_, err := op.GetStorageDetails(ctx, mock, true)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if count := atomic.LoadInt64(&mock.callCount); count != 1 {
		t.Fatalf("expected callCount 1, got %d", count)
	}

	// 2. Second fetch within 60s should be blocked by cooldown
	_, _ = op.GetStorageDetails(ctx, mock, true)
	if count := atomic.LoadInt64(&mock.callCount); count != 1 {
		t.Fatalf("expected callCount 1 during cooldown, got %d", count)
	}

	// 3. Invalidate storage state explicitly (simulates storage deletion/update)
	op.InvalidateStorageDetailsState(mock.MountPath)
	op.Cache.InvalidateStorageDetails(mock)

	// 4. Third fetch after state invalidation should bypass cooldown and hit driver
	_, err = op.GetStorageDetails(ctx, mock, true)
	if err != nil {
		t.Fatalf("third call failed: %v", err)
	}
	if count := atomic.LoadInt64(&mock.callCount); count != 2 {
		t.Fatalf("expected callCount 2 after InvalidateStorageDetailsState, got %d", count)
	}
}

func TestGetStorageDetailsRefreshBarrier(t *testing.T) {
	mock := &mockDriverWithDetails{
		Storage: model.Storage{
			MountPath:       "/test-mock-refresh-barrier",
			Status:          op.WORK,
			CacheExpiration: 30,
		},
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}

	setMockDetailsCooldown(t, "0")

	ctx := context.Background()

	// 1. 发起一个慢速的非强刷请求
	op.InvalidateStorageDetailsState(mock.MountPath)
	op.Cache.InvalidateStorageDetails(mock)
	var wg sync.WaitGroup
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(mock.releaseFirst) }) }
	t.Cleanup(func() { release(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = op.GetStorageDetails(ctx, mock, false)
	}()

	// Synchronize with the first driver call instead of assuming a sleep is enough.
	select {
	case <-mock.firstEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("ordinary probe did not enter driver")
	}

	// 2. 此时触发强刷请求，强刷不应复用慢速旧协程，而应通过代数屏障触发独立探测
	d2, err := op.GetStorageDetails(ctx, mock, true)
	if err != nil || d2.TotalSpace != 1000 {
		t.Fatalf("forced refresh call failed: %v", err)
	}

	release()
	wg.Wait()

	// 驱动调用次数应当为 2（一次普通请求，一次强刷请求，未被错误合并）
	if count := atomic.LoadInt64(&mock.callCount); count != 2 {
		t.Errorf("expected refresh barrier to cause 2 driver executions, got %d", count)
	}
}
