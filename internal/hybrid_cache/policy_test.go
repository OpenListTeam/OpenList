package hybrid_cache

import (
	"errors"
	"io"
	"os"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/mem"
)

func TestSelectPolicy(t *testing.T) {
	tests := []struct {
		name      string
		requested cache.Policy
		ceiling   int64
		want      cache.Policy
	}{
		{"explicit memory", cache.PolicyMemory, -1, cache.PolicyMemory},
		{"explicit disk", cache.PolicyDisk, 1024, cache.PolicyDisk},
		{"auto unknown", cache.PolicyAuto, -1, cache.PolicyDisk},
		{"auto empty", cache.PolicyAuto, 0, cache.PolicyMemory},
		{"auto known", cache.PolicyAuto, 1024, cache.PolicyMemory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectPolicy(tt.requested, tt.ceiling)
			if err != nil {
				t.Fatalf("selectPolicy() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("selectPolicy() = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := selectPolicy(cache.PolicyInherit, 1); err == nil {
		t.Fatal("selectPolicy() expected an error for inherit")
	}
}

func TestHybridCacheDiskPolicy(t *testing.T) {
	withCacheConfig(t, 0)
	hc, err := NewHybridCache(4, 8, cache.PolicyDisk)
	if err != nil {
		t.Fatalf("NewHybridCache() error = %v", err)
	}
	store, ok := hc.backingStore.(*singleFileStore)
	if !ok || hc.memoryStore != nil {
		t.Fatalf("disk policy initialized unexpected stores: memory=%T backing=%T", hc.memoryStore, hc.backingStore)
	}
	name := store.Name()
	if err := hc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache file still exists after Close(): %v", err)
	}
}

func TestHybridCachePolicyBackingSelection(t *testing.T) {
	tests := []struct {
		name           string
		requested      cache.Policy
		ceiling        int64
		budgetCapacity uint64
		wantMemory     bool
	}{
		{name: "memory ignores budget", requested: cache.PolicyMemory, ceiling: 8, wantMemory: true},
		{name: "disk uses disk", requested: cache.PolicyDisk, ceiling: 8},
		{name: "auto admitted uses memory", requested: cache.PolicyAuto, ceiling: 8, budgetCapacity: 8, wantMemory: true},
		{name: "auto rejected uses disk", requested: cache.PolicyAuto, ceiling: 8, budgetCapacity: 7},
		{name: "auto unknown uses disk", requested: cache.PolicyAuto, ceiling: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withCacheConfig(t, 8)
			hc, err := newHybridCache(4, tt.ceiling, tt.requested, mem.NewBudget(tt.budgetCapacity))
			if err != nil {
				t.Fatalf("newHybridCache() error = %v", err)
			}

			if hc.memoryBacking != tt.wantMemory {
				t.Fatalf("memory backing = %v, want %v", hc.memoryBacking, tt.wantMemory)
			}
			if _, isBuffer := hc.backingStore.(*BufferStore); isBuffer != tt.wantMemory {
				t.Fatalf("buffer backing = %v, want %v", isBuffer, tt.wantMemory)
			}
			if _, isFile := hc.backingStore.(*singleFileStore); isFile == tt.wantMemory {
				t.Fatalf("file backing = %v, want %v", isFile, !tt.wantMemory)
			}

			input := []byte("12345678")
			if tt.ceiling < 0 {
				input = []byte("unknown")
			}
			if _, err := hc.Write(input); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if hc.memoryOffset != 0 {
				t.Fatalf("linear memory offset = %d, want 0", hc.memoryOffset)
			}
			if hc.backingOffset != uint64(len(input)) {
				t.Fatalf("backing offset = %d, want %d", hc.backingOffset, len(input))
			}
			got := make([]byte, len(input))
			if _, err := hc.ReadAt(got, 0); err != nil {
				t.Fatalf("ReadAt() error = %v", err)
			}
			if string(got) != string(input) {
				t.Fatalf("cache = %q, want %q", got, input)
			}
			if err := hc.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		})
	}
}

func TestHybridCacheAutoRejectsWholeCeiling(t *testing.T) {
	withCacheConfig(t, 1024)
	hc, err := newHybridCache(4, 8, cache.PolicyAuto, mem.NewBudget(7))
	if err != nil {
		t.Fatalf("NewHybridCache() error = %v", err)
	}
	t.Cleanup(func() { _ = hc.Close() })
	if hc.memoryStore != nil || hc.memoryBacking {
		t.Fatalf("auto policy initialized memory stores: memory=%T backing=%T", hc.memoryStore, hc.backingStore)
	}
	if _, ok := hc.backingStore.(*singleFileStore); !ok {
		t.Fatalf("auto policy backing = %T, want singleFileStore", hc.backingStore)
	}
}

func TestHybridCacheZeroCeilingRejectsUnexpectedWrites(t *testing.T) {
	withCacheConfig(t, 8)
	hc, err := newHybridCache(4, 0, cache.PolicyAuto, mem.NewBudget(0))
	if err != nil {
		t.Fatalf("newHybridCache() error = %v", err)
	}
	t.Cleanup(func() { _ = hc.Close() })
	if _, err := hc.Write([]byte{1}); !errors.Is(err, mem.ErrNotEnoughMemory) {
		t.Fatalf("Write() error = %v, want ErrNotEnoughMemory", err)
	}
}

func TestHybridCacheStrictMemoryDoesNotSpill(t *testing.T) {
	withCacheConfig(t, 0)
	hc, err := NewHybridCache(4, 8, cache.PolicyMemory)
	if err != nil {
		t.Fatalf("NewHybridCache() error = %v", err)
	}
	t.Cleanup(func() { _ = hc.Close() })
	if _, err := hc.AllocBlock(4); err != nil {
		t.Fatalf("AllocBlock() error = %v", err)
	}
	if _, err := hc.AllocBlock(5); !errors.Is(err, mem.ErrNotEnoughMemory) {
		t.Fatalf("AllocBlock() error = %v, want ErrNotEnoughMemory", err)
	}
	if hc.backingStore != nil {
		t.Fatalf("strict memory policy spilled to %T", hc.backingStore)
	}
}

func TestHybridCacheUnknownMemory(t *testing.T) {
	withCacheConfig(t, 0)
	hc, err := NewHybridCache(3, -1, cache.PolicyMemory)
	if err != nil {
		t.Fatalf("NewHybridCache() error = %v", err)
	}
	t.Cleanup(func() { _ = hc.Close() })
	if _, ok := hc.backingStore.(*BufferStore); !ok {
		t.Fatalf("unknown memory cache backing = %T, want BufferStore", hc.backingStore)
	}
	if _, err := hc.Write([]byte("abcdefg")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	got := make([]byte, 7)
	if _, err := hc.ReadAt(got, 0); err != nil {
		t.Fatalf("ReadAt() error = %v", err)
	}
	if string(got) != "abcdefg" || hc.Size() != 7 {
		t.Fatalf("cache = %q size=%d", got, hc.Size())
	}
}

func TestHybridCacheReservationsPreventOversubscription(t *testing.T) {
	withCacheConfig(t, 8)
	budget := mem.NewBudget(8)
	first, err := newHybridCache(4, 8, cache.PolicyAuto, budget)
	if err != nil {
		t.Fatalf("first newHybridCache() error = %v", err)
	}
	if budget.Reserved() != 8 || !first.memoryBacking {
		t.Fatalf("first cache = memory:%v reserved:%d", first.memoryBacking, budget.Reserved())
	}

	second, err := newHybridCache(4, 8, cache.PolicyAuto, budget)
	if err != nil {
		t.Fatalf("second newHybridCache() error = %v", err)
	}
	if second.memoryBacking || budget.Reserved() != 8 {
		t.Fatalf("second cache = memory:%v reserved:%d", second.memoryBacking, budget.Reserved())
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if budget.Reserved() != 0 {
		t.Fatalf("reserved after close = %d", budget.Reserved())
	}

	third, err := newHybridCache(4, 8, cache.PolicyAuto, budget)
	if err != nil {
		t.Fatalf("third newHybridCache() error = %v", err)
	}
	if !third.memoryBacking {
		t.Fatal("third cache did not reuse released memory budget")
	}
	if err := third.Close(); err != nil {
		t.Fatalf("third Close() error = %v", err)
	}
}

func TestHybridCacheStrictMemoryIgnoresBudget(t *testing.T) {
	withCacheConfig(t, 8)
	budget := mem.NewBudget(0)
	hc, err := newHybridCache(4, 8, cache.PolicyMemory, budget)
	if err != nil {
		t.Fatalf("newHybridCache() error = %v", err)
	}
	if budget.Reserved() != 0 {
		t.Fatalf("strict memory reserved budget = %d, want 0", budget.Reserved())
	}
	if err := hc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestHybridCacheUnknownMemoryIgnoresBudget(t *testing.T) {
	withCacheConfig(t, 8)
	budget := mem.NewBudget(0)
	hc, err := newHybridCache(3, -1, cache.PolicyMemory, budget)
	if err != nil {
		t.Fatalf("newHybridCache() error = %v", err)
	}
	if _, err := hc.Write([]byte("1234567")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := hc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if budget.Reserved() != 0 {
		t.Fatalf("reserved after close = %d", budget.Reserved())
	}
}

func TestHybridCacheAutoSpillKeepsMemoryPrefix(t *testing.T) {
	withCacheConfig(t, 0)
	budget := mem.NewBudget(4)
	reservation, ok := budget.Reserve(4)
	if !ok {
		t.Fatal("Reserve(4) failed")
	}
	hc := &HybridCache{
		blockSize:            2,
		memoryStore:          &limitedMemory{buf: make([]byte, 0, 2)},
		spillOnMemoryFailure: true,
		memoryCeiling:        2,
		memoryReservation:    reservation,
	}
	t.Cleanup(func() { _ = hc.Close() })
	if _, err := hc.Write([]byte("abcd")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if hc.memoryOffset != 2 || hc.backingOffset != 2 {
		t.Fatalf("offsets = memory:%d disk:%d, want 2 and 2", hc.memoryOffset, hc.backingOffset)
	}
	if budget.Reserved() != 2 {
		t.Fatalf("reserved after spill = %d, want 2", budget.Reserved())
	}
	got := make([]byte, 4)
	if _, err := hc.ReadAt(got, 0); err != nil {
		t.Fatalf("ReadAt() error = %v", err)
	}
	if string(got) != "abcd" {
		t.Fatalf("cache = %q, want abcd", got)
	}
}

type limitedMemory struct {
	buf []byte
}

func (m *limitedMemory) Reallocate(size uint64) ([]byte, error) {
	if size > uint64(cap(m.buf)) {
		return nil, mem.ErrNotEnoughMemory
	}
	m.buf = m.buf[:size]
	return m.buf, nil
}

func (m *limitedMemory) Free() error {
	m.buf = nil
	return nil
}

func withCacheConfig(t *testing.T, autoMemoryLimit uint64) {
	t.Helper()
	oldConf := conf.Conf
	oldLimit := conf.AutoMemoryLimit
	oldBudgetCapacity := mem.CacheMemoryBudget.Capacity()
	oldBudgetReserved := mem.CacheMemoryBudget.Reserved()
	conf.Conf = &conf.Config{TempDir: t.TempDir()}
	conf.AutoMemoryLimit = autoMemoryLimit
	mem.CacheMemoryBudget.SetCapacity(1 << 40)
	t.Cleanup(func() {
		conf.Conf = oldConf
		conf.AutoMemoryLimit = oldLimit
		if reserved := mem.CacheMemoryBudget.Reserved(); reserved != oldBudgetReserved {
			t.Errorf("cache memory reserved after test = %d, want %d", reserved, oldBudgetReserved)
		}
		mem.CacheMemoryBudget.SetCapacity(oldBudgetCapacity)
	})
}

var _ io.Writer = (*HybridCache)(nil)
