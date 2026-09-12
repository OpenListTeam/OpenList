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
	errNoMemory := errors.New("no memory")
	tests := []struct {
		name      string
		requested cache.Policy
		ceiling   int64
		checkErr  error
		want      cache.Policy
		checks    int
	}{
		{"explicit memory", cache.PolicyMemory, -1, errNoMemory, cache.PolicyMemory, 0},
		{"explicit disk", cache.PolicyDisk, 1024, nil, cache.PolicyDisk, 0},
		{"auto unknown", cache.PolicyAuto, -1, nil, cache.PolicyDisk, 0},
		{"auto empty", cache.PolicyAuto, 0, nil, cache.PolicyMemory, 0},
		{"auto admitted", cache.PolicyAuto, 1024, nil, cache.PolicyMemory, 1},
		{"auto rejected", cache.PolicyAuto, 1024, errNoMemory, cache.PolicyDisk, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := 0
			got, err := selectPolicy(tt.requested, tt.ceiling, func(size uint64) error {
				checks++
				if size != uint64(tt.ceiling) {
					t.Fatalf("memory check size = %d, want %d", size, tt.ceiling)
				}
				return tt.checkErr
			})
			if err != nil {
				t.Fatalf("selectPolicy() error = %v", err)
			}
			if got != tt.want || checks != tt.checks {
				t.Fatalf("selectPolicy() = %q with %d checks, want %q with %d", got, checks, tt.want, tt.checks)
			}
		})
	}
	if _, err := selectPolicy(cache.PolicyInherit, 1, func(uint64) error { return nil }); err == nil {
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

func TestHybridCacheAutoRejectsWholeCeiling(t *testing.T) {
	withCacheConfig(t, 1024)
	hc, err := NewHybridCache(4, 8, cache.PolicyAuto)
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

func TestHybridCacheAutoSpillKeepsMemoryPrefix(t *testing.T) {
	withCacheConfig(t, 0)
	hc := &HybridCache{
		blockSize:            2,
		memoryStore:          &limitedMemory{buf: make([]byte, 0, 2)},
		spillOnMemoryFailure: true,
		memoryCeiling:        2,
	}
	t.Cleanup(func() { _ = hc.Close() })
	if _, err := hc.Write([]byte("abcd")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if hc.memoryOffset != 2 || hc.backingOffset != 2 {
		t.Fatalf("offsets = memory:%d disk:%d, want 2 and 2", hc.memoryOffset, hc.backingOffset)
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
	oldMinFreeMemory := conf.MinFreeMemory
	conf.Conf = &conf.Config{TempDir: t.TempDir()}
	conf.AutoMemoryLimit = autoMemoryLimit
	conf.MinFreeMemory = 0
	t.Cleanup(func() {
		conf.Conf = oldConf
		conf.AutoMemoryLimit = oldLimit
		conf.MinFreeMemory = oldMinFreeMemory
	})
}

var _ io.Writer = (*HybridCache)(nil)
