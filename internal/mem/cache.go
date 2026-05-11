package mem

import (
	"errors"
	"io"
	"os"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/pkg/buffer"
)

// 优先使用内存，失败后才使用文件。
// 线程不安全
type HybridCache struct {
	mem        LinearMemory
	memOffset  uint64
	file       *os.File
	fileOffset uint64
	blockSize  uint64
}

func (hc *HybridCache) NextBlockWithSize(size uint64) buffer.Block {
	if hc.file != nil {
		if hc.fileOffset > 0 && hc.file.Truncate(int64(hc.fileOffset+size)) != nil {
			return nil
		}
		base := hc.fileOffset
		hc.fileOffset += size
		fs := buffer.NewBlockAdapter(
			io.NewOffsetWriter(hc.file, int64(base)),
			io.NewSectionReader(hc.file, int64(base), int64(size)),
		)
		return fs
	}
	all, err := hc.mem.Reallocate(uint64(hc.memOffset + size))
	if err == nil {
		start := hc.memOffset
		hc.memOffset += size
		return buffer.NewByteBlock(all[start : start+size])
	}
	if err := hc.initFileCache(); err != nil {
		return nil
	}
	return hc.NextBlockWithSize(size)
}

func (hc *HybridCache) NextBlock() buffer.Block {
	return hc.NextBlockWithSize(hc.blockSize)
}

// func (hc *HybridCache) GetBlockSize() uint64 {
// 	return hc.blockSize
// }

func (hc *HybridCache) RollbackBlockWithSize(size uint64) {
	if hc.fileOffset > size {
		hc.fileOffset -= size
		return
	}
	size -= hc.fileOffset
	hc.fileOffset = 0
	if hc.memOffset > size {
		hc.memOffset -= size
		return
	}
	hc.memOffset = 0
}

func (hc *HybridCache) RollbackBlock() {
	hc.RollbackBlockWithSize(hc.blockSize)
}

func (hc *HybridCache) initFileCache() error {
	f, err := os.CreateTemp(conf.Conf.TempDir, "file-*")
	if err != nil {
		return err
	}
	if err := f.Truncate(int64(hc.blockSize)); err != nil {
		_, _ = f.Close(), os.Remove(f.Name())
		return err
	}
	hc.file = f
	return nil
}

func (hc *HybridCache) Close() error {
	if hc.blockSize > 0 {
		hc.blockSize = 0
		var err error
		if hc.mem != nil {
			err = hc.mem.Free()
			hc.mem = nil
		}
		if hc.file != nil {
			err = errors.Join(err, hc.file.Close(), os.Remove(hc.file.Name()))
			hc.file = nil
		}
		return err
	}
	return nil
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

// 优先使用内存，失败后才使用文件
// 线程不安全
func NewHybridCache(blockSize, maxMemorySize uint64) (*HybridCache, error) {
	var err error
	if conf.CacheThreshold > 0 {
		var m LinearMemory
		m, err = NewGuardedMemory(blockSize, maxMemorySize)
		if err == nil {
			return &HybridCache{mem: m, blockSize: blockSize}, nil
		}
	}
	hc := &HybridCache{blockSize: blockSize}
	if err2 := hc.initFileCache(); err2 != nil {
		return nil, errors.Join(err, err2)
	}
	return hc, nil
}

var _ buffer.Block = (*HybridCache)(nil)
