package mem

import (
	"errors"
	"io"
	"os"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/pkg/buffer"
)

type fileSection struct {
	*io.OffsetWriter
	*io.SectionReader
}

func (fs *fileSection) GetSectionWriter() buffer.SectionWriter {
	return fs.OffsetWriter
}

func (fs *fileSection) GetSectionReader() buffer.SectionReader {
	return fs.SectionReader
}

var _ buffer.Section = (*fileSection)(nil)

// 优先使用内存，失败后才使用文件
// 线程不安全
type HybridCache struct {
	mem            LinearMemory
	memOffset      uint64
	maxSectionSize uint64
	file           *os.File
	fileOffset     uint64
	cache          []buffer.Section
}

func (hc *HybridCache) NewSectionWithSize(size uint64) buffer.Section {
	if hc.file != nil {
		if hc.fileOffset > 0 && hc.file.Truncate(int64(hc.fileOffset+size)) != nil {
			return nil
		}
		base := hc.fileOffset
		hc.fileOffset += size
		fs := &fileSection{io.NewOffsetWriter(hc.file, int64(base)), io.NewSectionReader(hc.file, int64(base), int64(size))}
		return fs
	}
	all, err := hc.mem.Reallocate(uint64(hc.memOffset + size))
	if err == nil {
		start := hc.memOffset
		hc.memOffset += size
		return buffer.NewByteSection(all[start : start+size])
	}
	if err := hc.initFileCache(); err != nil {
		return nil
	}
	return hc.NewSectionWithSize(size)
}

func (hc *HybridCache) NewSection() buffer.Section {
	return hc.NewSectionWithSize(hc.maxSectionSize)
}

func (hc *HybridCache) initFileCache() error {
	f, err := os.CreateTemp(conf.Conf.TempDir, "file-*")
	if err != nil {
		return err
	}
	if err := f.Truncate(int64(hc.maxSectionSize)); err != nil {
		_, _ = f.Close(), os.Remove(f.Name())
		return err
	}
	hc.file = f
	return nil
}

func (hc *HybridCache) Get() buffer.Section {
	if len(hc.cache) == 0 {
		return hc.NewSection()
	}
	item := hc.cache[len(hc.cache)-1]
	hc.cache = hc.cache[:len(hc.cache)-1]
	return item
}

func (hc *HybridCache) Put(s buffer.Section) {
	hc.cache = append(hc.cache, s)
}

func (hc *HybridCache) Close() error {
	if hc.maxSectionSize > 0 {
		hc.maxSectionSize = 0
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

// 优先使用内存，失败后才使用文件
// 线程不安全
func NewHybridCache(maxSectionSize, maxMemorySize uint64) (*HybridCache, error) {
	var err error
	if conf.MmapThreshold > 0 {
		var m LinearMemory
		m, err = NewGuardedMemory(maxSectionSize, maxMemorySize)
		if err == nil {
			return &HybridCache{mem: m, maxSectionSize: maxSectionSize}, nil
		}
	}
	hc := &HybridCache{maxSectionSize: maxSectionSize}
	if err2 := hc.initFileCache(); err2 != nil {
		return nil, errors.Join(err, err2)
	}
	return hc, nil
}

type HybridCacheReader struct {
	hc     *HybridCache
	offset int64
}

func NewHybridCacheReader(hc *HybridCache) *HybridCacheReader {
	return &HybridCacheReader{hc: hc}
}

func (hcr *HybridCacheReader) Size() int64 {
	return int64(hcr.hc.memOffset + hcr.hc.fileOffset)
}

func (hcr *HybridCacheReader) Read(p []byte) (n int, err error) {
	n, err = hcr.ReadAt(p, hcr.offset)
	if n > 0 {
		hcr.offset += int64(n)
	}
	return n, err
}

func (hcr *HybridCacheReader) ReadAt(p []byte, off int64) (n int, err error) {
	if off < 0 || off >= hcr.Size() {
		return 0, io.EOF
	}
	if off < int64(hcr.hc.memOffset) {
		all, err := hcr.hc.mem.Reallocate(min(hcr.hc.memOffset, uint64(off)+uint64(len(p))))
		if err != nil {
			// 不可能失败
			panic(err)
		}
		n = copy(p, all[off:])
		if n == len(p) {
			return n, nil
		}
	}
	if hcr.hc.file == nil {
		return n, io.EOF
	}
	nn, err := hcr.hc.file.ReadAt(p[n:], off+int64(n)-int64(hcr.hc.memOffset))
	return n + nn, err
}

func (hcr *HybridCacheReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		if offset == 0 {
			return hcr.offset, nil
		}
		offset = hcr.offset + offset
	case io.SeekEnd:
		offset = hcr.Size() + offset
	default:
		return 0, errors.New("Seek: invalid whence")
	}

	if offset < 0 || offset > hcr.Size() {
		return 0, errors.New("Seek: invalid offset")
	}
	hcr.offset = offset
	return offset, nil
}
