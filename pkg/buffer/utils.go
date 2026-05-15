package buffer

import (
	"errors"
	"io"
)

type WriteAtSeekerProvider interface{ GetWriteAtSeeker() WriteAtSeeker }

func WriteAtSeekerOf(b Block) WriteAtSeeker {
	if p, ok := b.(WriteAtSeekerProvider); ok {
		return p.GetWriteAtSeeker()
	}
	return io.NewOffsetWriter(b, 0)
}

type ReadAtSeekerProvider interface{ GetReadAtSeeker() ReadAtSeeker }

// 将一个Block包装为ReadAtSeeker。
// 固定大小：当前Block的Size()。
func ReadAtSeekerOf(b Block) ReadAtSeeker {
	if p, ok := b.(ReadAtSeekerProvider); ok {
		return p.GetReadAtSeeker()
	}
	return io.NewSectionReader(b, 0, b.Size())
}

type blockAdapter struct {
	WriteAtSeeker
	SizedReadAtSeeker
}

func (b *blockAdapter) GetWriteAtSeeker() WriteAtSeeker {
	return b.WriteAtSeeker
}

func (b *blockAdapter) GetReadAtSeeker() ReadAtSeeker {
	return b.SizedReadAtSeeker
}
func NewBlockAdapter(w WriteAtSeeker, r SizedReadAtSeeker) Block {
	return &blockAdapter{
		WriteAtSeeker:     w,
		SizedReadAtSeeker: r,
	}
}

var _ Block = (*blockAdapter)(nil)

// 将一个Block包装为ReadAtSeeker。
// 动态大小：Size() 是动态跟随底层 Block。
type DynamicReadAtSeeker struct {
	block  Block
	offset int64
}

func (r *DynamicReadAtSeeker) ReadAt(p []byte, off int64) (n int, err error) {
	return r.block.ReadAt(p, off)
}

func (r *DynamicReadAtSeeker) Read(p []byte) (n int, err error) {
	n, err = r.block.ReadAt(p, r.offset)
	if n > 0 {
		r.offset += int64(n)
	}
	return n, err
}

func (r *DynamicReadAtSeeker) Size() int64 {
	return r.block.Size()
}

func (r *DynamicReadAtSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		if offset == 0 {
			return r.offset, nil
		}
		offset = r.offset + offset
	case io.SeekEnd:
		offset = r.block.Size() + offset
	default:
		return 0, errors.New("Seek: invalid whence")
	}

	if offset < 0 || offset > r.block.Size() {
		return 0, errors.New("Seek: invalid offset")
	}
	r.offset = offset
	return offset, nil
}

func NewDynamicReadAtSeeker(block Block) *DynamicReadAtSeeker {
	return &DynamicReadAtSeeker{
		block: block,
	}
}
