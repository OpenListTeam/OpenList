package mem

import (
	"errors"
	"io"
	"runtime"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/pkg/buffer"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// 优先使用内存，失败后才使用文件。
// 线程不安全
type HybridCache struct {
	mem        LinearMemory
	memOffset  uint64
	file       cache.FileCache
	fileOffset uint64
	blockSize  uint64
}

func (hc *HybridCache) NextBlockWithSize(size uint64) (buffer.Block, error) {
retry:
	if hc.file != nil {
		if err := hc.file.Truncate(int64(hc.fileOffset + size)); err != nil {
			return nil, err
		}
		base := hc.fileOffset
		hc.fileOffset += size
		fs := buffer.NewBlockAdapter(
			io.NewOffsetWriter(hc.file, int64(base)),
			io.NewSectionReader(hc.file, int64(base), int64(size)),
		)
		return fs, nil
	}
	all, err := hc.mem.Reallocate(hc.memOffset + size)
	if err == nil {
		start := hc.memOffset
		hc.memOffset += size
		return buffer.NewByteBlock(all[start : start+size]), nil
	}
	if err := hc.initFileCache(); err != nil {
		return nil, err
	}
	goto retry
}

func (hc *HybridCache) NextBlock() (buffer.Block, error) {
	return hc.NextBlockWithSize(hc.blockSize)
}

func (hc *HybridCache) RollbackBlockWithSize(size uint64) {
	if hc.fileOffset >= size {
		hc.fileOffset -= size
		return
	}
	size -= hc.fileOffset
	hc.fileOffset = 0
	if hc.memOffset >= size {
		hc.memOffset -= size
		return
	}
	hc.memOffset = 0
}

func (hc *HybridCache) RollbackBlock() {
	hc.RollbackBlockWithSize(hc.blockSize)
}

func (hc *HybridCache) initFileCache() error {
	file, err := cache.NewFileCache(int64(hc.blockSize))
	if err != nil {
		return err
	}
	hc.file = file
	return nil
}

func (hc *HybridCache) Close() error {
	var err error
	if hc.mem != nil {
		err = hc.mem.Free()
		hc.mem = nil
	}
	if hc.file != nil {
		err = errors.Join(err, hc.file.Close())
		hc.file = nil
	}
	return err
}

func (hc *HybridCache) Size() int64 {
	return int64(hc.memOffset + hc.fileOffset)
}

func (hc *HybridCache) ReadAt(p []byte, off int64) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off >= hc.Size() {
		return 0, io.EOF
	}
	if off < int64(hc.memOffset) {
		all, err := hc.mem.Reallocate(min(hc.memOffset, uint64(off)+uint64(len(p))))
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

	off += int64(n) - int64(hc.memOffset)
	canRead := int64(hc.fileOffset) - off
	if canRead <= 0 {
		return n, io.EOF
	}
	nn, err := hc.file.ReadAt(p[:min(len(p), int(canRead))], off)
	return n + nn, err
}

func (hc *HybridCache) WriteAt(p []byte, off int64) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off >= hc.Size() {
		return 0, io.ErrShortWrite
	}

	if off < int64(hc.memOffset) {
		all, err := hc.mem.Reallocate(min(hc.memOffset, uint64(off)+uint64(len(p))))
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

	off += int64(n) - int64(hc.memOffset)
	canWrite := int64(hc.fileOffset) - off
	if canWrite <= 0 {
		return n, io.ErrShortWrite
	}
	nn, err := hc.file.WriteAt(p[:min(len(p), int(canWrite))], off)
	return n + nn, err
}

func (hc *HybridCache) CopyFromN(src io.Reader, n int64) (written int64, err error) {
	limit := n
	for limit > 0 {
		blockSize := limit
		if hc.file == nil && blockSize > int64(conf.MaxBlockLimit) {
			blockSize = int64(conf.MaxBlockLimit)
		}
		b, err := hc.NextBlockWithSize(uint64(blockSize))
		if err != nil {
			return written, err
		}
		nn, err := utils.CopyWithBufferN(buffer.WriteAtSeekerOf(b), src, blockSize)
		written += nn
		if nn != blockSize {
			return written, err
		}
		limit -= nn
	}
	return written, nil
}

// 优先使用内存，失败后才使用文件
// 线程不安全
func NewHybridCache(blockSize, maxMemorySize uint64) (*HybridCache, error) {
	var err error
	var hc *HybridCache
	if conf.MinFreeMemory > 0 && maxMemorySize >= blockSize {
		var m LinearMemory
		m, err = NewGuardedMemory(blockSize, maxMemorySize)
		if err == nil {
			hc = &HybridCache{mem: m, blockSize: blockSize}
		}
	}
	if hc == nil {
		hc = &HybridCache{blockSize: blockSize}
		if err2 := hc.initFileCache(); err2 != nil {
			return nil, errors.Join(err, err2)
		}
	}
	runtime.SetFinalizer(hc, func(hc *HybridCache) {
		if hc.file != nil {
			_ = hc.file.Close()
			hc.file = nil
		}
	})
	return hc, nil
}

var _ buffer.Block = (*HybridCache)(nil)
