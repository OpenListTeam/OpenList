//go:build !unix && !windows

package mem // import "github.com/ncruces/go-sqlite3/internal/alloc"

func NewMemory(cap, max uint64) (LinearMemory, error) {
	return &sliceMemory{make([]byte, 0, cap)}, nil
}

type sliceMemory struct {
	buf []byte
}

func (b *sliceMemory) Free() error {
	b.buf = nil
	return nil
}

func (b *sliceMemory) Reallocate(size uint64) ([]byte, error) {
	if cap := uint64(cap(b.buf)); size > cap {
		b.buf = append(b.buf[:cap], make([]byte, size-cap)...)
	} else {
		b.buf = b.buf[:size]
	}
	return b.buf, nil
}
