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
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off >= r.size {
		return 0, io.EOF
	}

	n := 0
	for _, buf := range r.bufs {
		if off >= int64(len(buf)) {
			off -= int64(len(buf))
			continue
		}
		nn := copy(p[n:], buf[off:])
		n += nn
		if n == len(p) {
			return n, nil
		}
		off = 0
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

func (r *Reader) Close() error {
	r.Reset()
	return nil
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

type byteBlock struct {
	buf []byte
}

func NewByteBlock(buf []byte) Block {
	return &byteBlock{buf: buf}
}

func (b *byteBlock) Size() int64 {
	return int64(len(b.buf))
}

func (b *byteBlock) ReadAt(p []byte, off int64) (n int, err error) {
	if len(b.buf) == 0 || off < 0 || off >= b.Size() {
		return 0, io.EOF
	}
	n = copy(p, b.buf[off:])
	if n < len(p) {
		err = io.EOF
	}
	return
}

func (b *byteBlock) WriteAt(p []byte, off int64) (n int, err error) {
	if len(b.buf) == 0 || off < 0 || off >= b.Size() {
		return 0, io.ErrShortWrite
	}
	n = copy(b.buf[off:], p)
	if n < len(p) {
		err = io.ErrShortWrite
	}
	return
}

var _ Block = (*byteBlock)(nil)
