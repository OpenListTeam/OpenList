package op_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type mockDriverWithDetails struct {
	model.Storage
	callCount int64
	delay     time.Duration
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

func (m *mockDriverWithDetails) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	atomic.AddInt64(&m.callCount, 1)
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
	_ = op.SaveSettingItem(&model.SettingItem{
		Key:   conf.StorageDetailsCooldownSeconds,
		Value: "0",
		Type:  conf.TypeNumber,
		Group: model.STYLE,
	})

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
	_ = op.SaveSettingItem(&model.SettingItem{
		Key:   conf.StorageDetailsCooldownSeconds,
		Value: "3",
		Type:  conf.TypeNumber,
		Group: model.STYLE,
	})

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
