package hybrid_cache

import (
	"errors"
	"io"
	"runtime"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/mem"
	"github.com/OpenListTeam/OpenList/v4/pkg/buffer"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// 线程不安全，单线程使用，或者外部加锁保护
type HybridCache struct {
	blockSize     uint64
	memoryStore   mem.LinearMemory
	memoryOffset  uint64
	backingStore  BackingStore
	backingOffset uint64
}

// HybridCache本身是一个大的Block，支持分块成多个小的Block

// 分配一个新的Block，支持读写，大小为size
func (hc *HybridCache) AllocBlock(size uint64) (buffer.Block, error) {
retry:
	if hc.backingStore != nil {
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
	all, err := hc.memoryStore.Reallocate(hc.memoryOffset + size)
	if err == nil {
		start := hc.memoryOffset
		hc.memoryOffset += size
		return buffer.NewByteBlock(all[start : start+size]), nil
	}
	if err2 := hc.initFileCache(); err2 != nil {
		return nil, errors.Join(err, err2)
	}
	goto retry
}

func (hc *HybridCache) allocWriteAtSeeker(size uint64) (buffer.WriteAtSeeker, error) {
retry:
	if hc.backingStore != nil {
		if err := hc.backingStore.GrowTo(int64(hc.backingOffset + size)); err != nil {
			return nil, err
		}
		base := hc.backingOffset
		hc.backingOffset += size
		return io.NewOffsetWriter(hc.backingStore, int64(base)), nil
	}
	all, err := hc.memoryStore.Reallocate(hc.memoryOffset + size)
	if err == nil {
		start := hc.memoryOffset
		hc.memoryOffset += size
		return io.NewOffsetWriter(buffer.NewByteBlock(all[start:start+size]), 0), nil
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

func (hc *HybridCache) initFileCache() error {
	file, err := NewFileStore(int64(hc.blockSize))
	if err != nil {
		return err
	}
	hc.backingStore = file
	return nil
}

func (hc *HybridCache) Close() error {
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

// HybridCache 线程不安全，单线程使用，或者外部加锁保护
func NewHybridCache(blockSize, maxMemorySize uint64) (hc *HybridCache, err error) {
	if conf.MinFreeMemory > 0 {
		// 策略1: Go自动内存管理
		if maxMemorySize <= conf.AutoMemoryLimit {
			return &HybridCache{backingStore: &BufferStore{}, blockSize: blockSize}, nil
		}

		// 策略2: 手动内存管理
		if maxMemorySize >= blockSize {
			var m mem.LinearMemory
			// 手动管理内存，Uinx Mmap 或者 Windows VirtualAlloc
			if m, err = mem.NewGuardedMemory(blockSize, maxMemorySize); err == nil {
				hc = &HybridCache{memoryStore: m, blockSize: blockSize}
			}
		}
	}
	// 策略3: 文件后备
	if hc == nil {
		hc = &HybridCache{blockSize: blockSize}
		// 文件
		if err2 := hc.initFileCache(); err2 != nil {
			return nil, errors.Join(err, err2)
		}
	}
	runtime.SetFinalizer(hc, func(hc *HybridCache) {
		if hc.backingStore != nil {
			_ = hc.backingStore.Close()
			hc.backingStore = nil
		}
	})
	return hc, nil
}

var _ buffer.Block = (*HybridCache)(nil)
