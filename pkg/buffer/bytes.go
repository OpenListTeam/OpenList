package buffer

import (
	"errors"
	"io"
)

// 用于存储不复用的[]byte
type Reader struct {
	bufs   [][]byte
	size   int64
	offset int64
}

func (r *Reader) Size() int64 {
	return r.size
}

func (r *Reader) Append(buf []byte) {
	r.size += int64(len(buf))
	r.bufs = append(r.bufs, buf)
}

func (r *Reader) Read(p []byte) (int, error) {
	n, err := r.ReadAt(p, r.offset)
	if n > 0 {
		r.offset += int64(n)
	}
	return n, err
}

func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= r.size {
		return 0, io.EOF
	}

	n := 0
	readFrom := false
	for _, buf := range r.bufs {
		if readFrom {
			nn := copy(p[n:], buf)
			n += nn
			if n == len(p) {
				return n, nil
			}
		} else if newOff := off - int64(len(buf)); newOff >= 0 {
			off = newOff
		} else {
			nn := copy(p, buf[off:])
			if nn == len(p) {
				return nn, nil
			}
			n += nn
			readFrom = true
		}
	}

	return n, io.EOF
}

func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset = r.offset + offset
	case io.SeekEnd:
		offset = r.size + offset
	default:
		return 0, errors.New("Seek: invalid whence")
	}

	if offset < 0 || offset > r.size {
		return 0, errors.New("Seek: invalid offset")
	}

	r.offset = offset
	return offset, nil
}

func (r *Reader) Reset() {
	clear(r.bufs)
	r.bufs = nil
	r.size = 0
	r.offset = 0
}

func NewReader(buf ...[]byte) *Reader {
	b := &Reader{
		bufs: make([][]byte, 0, len(buf)),
	}
	for _, b1 := range buf {
		b.Append(b1)
	}
	return b
}
