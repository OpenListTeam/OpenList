package hybrid_cache

import (
	"errors"
	"fmt"
	"io"
	"runtime"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/mem"
	"github.com/OpenListTeam/OpenList/v4/pkg/buffer"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// 线程不安全，单线程使用，或者外部加锁保护
type HybridCache struct {
	blockSize            uint64
	memoryStore          mem.LinearMemory
	memoryOffset         uint64
	backingStore         BackingStore
	backingOffset        uint64
	cleanup              runtime.Cleanup
	spillOnMemoryFailure bool
	memoryBacking        bool
	memoryCeiling        int64
}

// HybridCache本身是一个大的Block，支持分块成多个小的Block

// 分配一个新的Block，支持读写，大小为size
func (hc *HybridCache) AllocBlock(size uint64) (buffer.Block, error) {
retry:
	if hc.backingStore != nil {
		if hc.memoryBacking && hc.exceedsMemoryCeiling(hc.backingOffset, size) {
			return nil, mem.ErrNotEnoughMemory
		}
		if err := hc.backingStore.GrowTo(int64(hc.backingOffset + size)); err != nil {
			return nil, err
		}
		base := hc.backingOffset
		hc.backingOffset += size
		fs := buffer.NewBlockAdapter(
			io.NewOffsetWriter(hc.backingStore, int64(base)),
			io.NewSectionReader(hc.backingStore, int64(base), int64(size)),
		)
		return fs, nil
	}
	var all []byte
	var err error
	if hc.exceedsMemoryCeiling(hc.memoryOffset, size) {
		err = mem.ErrNotEnoughMemory
	} else {
		all, err = hc.memoryStore.Reallocate(hc.memoryOffset + size)
	}
	if err == nil {
		start := hc.memoryOffset
		hc.memoryOffset += size
		return buffer.NewByteBlock(all[start : start+size]), nil
	}
	if !hc.spillOnMemoryFailure {
		return nil, err
	}
	if err2 := hc.initFileCache(); err2 != nil {
		return nil, errors.Join(err, err2)
	}
	goto retry
}

func (hc *HybridCache) allocWriteAtSeeker(size uint64) (buffer.WriteAtSeeker, error) {
retry:
	if hc.backingStore != nil {
		if hc.memoryBacking && hc.exceedsMemoryCeiling(hc.backingOffset, size) {
			return nil, mem.ErrNotEnoughMemory
		}
		if err := hc.backingStore.GrowTo(int64(hc.backingOffset + size)); err != nil {
			return nil, err
		}
		base := hc.backingOffset
		hc.backingOffset += size
		return io.NewOffsetWriter(hc.backingStore, int64(base)), nil
	}
	var all []byte
	var err error
	if hc.exceedsMemoryCeiling(hc.memoryOffset, size) {
		err = mem.ErrNotEnoughMemory
	} else {
		all, err = hc.memoryStore.Reallocate(hc.memoryOffset + size)
	}
	if err == nil {
		start := hc.memoryOffset
		hc.memoryOffset += size
		return io.NewOffsetWriter(buffer.NewByteBlock(all[start:start+size]), 0), nil
	}
	if !hc.spillOnMemoryFailure {
		return nil, err
	}
	if err2 := hc.initFileCache(); err2 != nil {
		return nil, errors.Join(err, err2)
	}
	goto retry
}

func (hc *HybridCache) NextBlock() (buffer.Block, error) {
	return hc.AllocBlock(hc.blockSize)
}

func (hc *HybridCache) RewindBySize(size uint64) {
	if hc.backingOffset >= size {
		hc.backingOffset -= size
		return
	}
	size -= hc.backingOffset
	hc.backingOffset = 0
	if hc.memoryOffset >= size {
		hc.memoryOffset -= size
		return
	}
	size -= hc.memoryOffset
	hc.memoryOffset = 0
}

func (hc *HybridCache) RewindOneBlock() {
	hc.RewindBySize(hc.blockSize)
}

func (hc *HybridCache) exceedsMemoryCeiling(offset, size uint64) bool {
	if hc.memoryCeiling < 0 {
		return false
	}
	ceiling := uint64(hc.memoryCeiling)
	return offset > ceiling || size > ceiling-offset
}

func (hc *HybridCache) initFileCache() error {
	initialSize := hc.blockSize
	if hc.memoryCeiling < 0 {
		initialSize = 0
	}
	file, err := NewFileStore(int64(initialSize))
	if err != nil {
		return err
	}
	hc.cleanup = runtime.AddCleanup(hc, func(file BackingStore) {
		_ = file.Close()
	}, file)
	hc.backingStore = file
	return nil
}

func (hc *HybridCache) Close() error {
	hc.cleanup.Stop()
	var err error
	if hc.memoryStore != nil {
		err = hc.memoryStore.Free()
		hc.memoryStore = nil
		hc.memoryOffset = 0
	}
	if hc.backingStore != nil {
		err = errors.Join(err, hc.backingStore.Close())
		hc.backingStore = nil
		hc.backingOffset = 0
	}
	return err
}

func (hc *HybridCache) Size() int64 {
	return int64(hc.memoryOffset + hc.backingOffset)
}

func (hc *HybridCache) ReadAt(p []byte, off int64) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off >= hc.Size() {
		return 0, io.EOF
	}

	if off < int64(hc.memoryOffset) {
		all, err := hc.memoryStore.Reallocate(min(hc.memoryOffset, uint64(off)+uint64(len(p))))
		if err != nil {
			// 不可能失败
			panic(err)
		}
		n = copy(p, all[off:])
		if n == len(p) {
			return n, nil
		}
		p = p[n:]
	}

	off += int64(n) - int64(hc.memoryOffset)
	canRead := int64(hc.backingOffset) - off
	if canRead <= 0 {
		return n, io.EOF
	}
	nn, err := hc.backingStore.ReadAt(p[:min(len(p), int(canRead))], off)
	return n + nn, err
}

func (hc *HybridCache) WriteAt(p []byte, off int64) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off >= hc.Size() {
		return 0, io.ErrShortWrite
	}

	if off < int64(hc.memoryOffset) {
		all, err := hc.memoryStore.Reallocate(min(hc.memoryOffset, uint64(off)+uint64(len(p))))
		if err != nil {
			// 不可能失败
			panic(err)
		}
		n = copy(all[off:], p)
		if n == len(p) {
			return n, nil
		}
		p = p[n:]
	}

	off += int64(n) - int64(hc.memoryOffset)
	canWrite := int64(hc.backingOffset) - off
	if canWrite <= 0 {
		return n, io.ErrShortWrite
	}
	nn, err := hc.backingStore.WriteAt(p[:min(len(p), int(canWrite))], off)
	return n + nn, err
}

// Write appends p to the cache. It is not safe for concurrent use.
func (hc *HybridCache) Write(p []byte) (n int, err error) {
	for len(p) > 0 {
		chunkSize := len(p)
		if hc.blockSize > 0 && uint64(chunkSize) > hc.blockSize {
			chunkSize = int(hc.blockSize)
		}
		w, allocErr := hc.allocWriteAtSeeker(uint64(chunkSize))
		if allocErr != nil {
			return n, allocErr
		}
		nn, writeErr := w.Write(p[:chunkSize])
		n += nn
		if nn < chunkSize {
			hc.RewindBySize(uint64(chunkSize - nn))
			if writeErr == nil {
				writeErr = io.ErrShortWrite
			}
		}
		if writeErr != nil {
			return n, writeErr
		}
		p = p[chunkSize:]
	}
	return n, nil
}

func (hc *HybridCache) CopyFromN(src io.Reader, n int64) (written int64, err error) {
	limit := n
	for limit > 0 {
		blockSize := limit
		if hc.backingStore == nil && blockSize > int64(conf.MaxBlockLimit) {
			blockSize = int64(conf.MaxBlockLimit)
		}
		b, err := hc.allocWriteAtSeeker(uint64(blockSize))
		if err != nil {
			return written, err
		}
		nn, err := utils.CopyWithBufferN(b, src, blockSize)
		written += nn
		if nn != blockSize {
			return written, err
		}
		limit -= nn
	}
	return written, nil
}

type memoryCheck func(uint64) error

func selectPolicy(requested cache.Policy, memoryCeiling int64, check memoryCheck) (cache.Policy, error) {
	if !requested.IsConcrete() {
		return cache.PolicyInherit, fmt.Errorf("invalid cache policy %q", requested)
	}
	switch requested {
	case cache.PolicyMemory, cache.PolicyDisk:
		return requested, nil
	case cache.PolicyAuto:
		if memoryCeiling < 0 {
			return cache.PolicyDisk, nil
		}
		if memoryCeiling == 0 {
			return cache.PolicyMemory, nil
		}
		if err := check(uint64(memoryCeiling)); err != nil {
			return cache.PolicyDisk, nil
		}
		return cache.PolicyMemory, nil
	default:
		panic("unreachable")
	}
}

// SelectPolicy resolves auto to a concrete memory or disk policy for a cache
// whose maximum simultaneous memory footprint is memoryCeiling. A negative
// ceiling means that the upper bound is unknown.
func SelectPolicy(requested cache.Policy, memoryCeiling int64) (cache.Policy, error) {
	return selectPolicy(requested, memoryCeiling, mem.MemoryGrowCheck)
}

// NewHybridCache creates a non-thread-safe cache using the requested policy.
func NewHybridCache(blockSize uint64, memoryCeiling int64, requested cache.Policy) (hc *HybridCache, err error) {
	if memoryCeiling < 0 && blockSize == 0 {
		return nil, fmt.Errorf("block size must be positive when memory ceiling is unknown")
	}
	if memoryCeiling > 0 {
		blockSize = min(blockSize, uint64(memoryCeiling))
		if blockSize == 0 {
			return nil, fmt.Errorf("block size must be positive for a non-empty cache")
		}
	}

	selected, err := SelectPolicy(requested, memoryCeiling)
	if err != nil {
		return nil, err
	}
	hc = &HybridCache{blockSize: blockSize, memoryCeiling: memoryCeiling}
	if selected == cache.PolicyDisk {
		if err := hc.initFileCache(); err != nil {
			return nil, err
		}
		return hc, nil
	}

	if memoryCeiling < 0 || uint64(memoryCeiling) <= conf.AutoMemoryLimit {
		hc.backingStore = &BufferStore{}
		hc.memoryBacking = true
		return hc, nil
	}

	if requested == cache.PolicyMemory {
		hc.memoryStore, err = mem.NewManagedMemory(blockSize, uint64(memoryCeiling), nil)
		if err != nil {
			return nil, err
		}
		return hc, nil
	}

	hc.memoryStore, err = mem.NewGuardedMemory(blockSize, uint64(memoryCeiling))
	if err == nil {
		hc.spillOnMemoryFailure = true
		return hc, nil
	}

	if fileErr := hc.initFileCache(); fileErr != nil {
		return nil, errors.Join(err, fileErr)
	}
	return hc, nil
}

var _ buffer.Block = (*HybridCache)(nil)
var _ io.Writer = (*HybridCache)(nil)
