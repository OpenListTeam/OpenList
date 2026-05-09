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

func (hc *HybridCache) NewWithSize(size uint64) buffer.Section {
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
	return hc.NewWithSize(size)
}

func (hc *HybridCache) New() buffer.Section {
	return hc.NewWithSize(hc.maxSectionSize)
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
		return hc.New()
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
	m, err := NewGuardedMemory(maxSectionSize, maxMemorySize)
	if err == nil {
		return &HybridCache{mem: m, maxSectionSize: maxSectionSize}, nil
	}
	hc := &HybridCache{maxSectionSize: maxSectionSize}
	if err2 := hc.initFileCache(); err2 != nil {
		return nil, errors.Join(err, err2)
	}
	return hc, nil
}
