package buffer

import (
	"errors"
	"io"
)

// 用于存储不复用的[]byte
type Reader struct {
	bufs   [][]byte
	length int
	offset int
}

func (r *Reader) Len() int {
	return r.length
}

func (r *Reader) Append(buf []byte) {
	r.length += len(buf)
	r.bufs = append(r.bufs, buf)
}

func (r *Reader) Read(p []byte) (int, error) {
	n, err := r.ReadAt(p, int64(r.offset))
	if n > 0 {
		r.offset += n
	}
	return n, err
}

func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(r.length) {
		return 0, io.EOF
	}

	n, length := 0, int64(0)
	readFrom := false
	for _, buf := range r.bufs {
		newLength := length + int64(len(buf))
		if readFrom {
			w := copy(p[n:], buf)
			n += w
		} else if off < newLength {
			readFrom = true
			w := copy(p[n:], buf[int(off-length):])
			n += w
		}
		if n == len(p) {
			return n, nil
		}
		length = newLength
	}

	return n, io.EOF
}

func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	var abs int
	switch whence {
	case io.SeekStart:
		abs = int(offset)
	case io.SeekCurrent:
		abs = r.offset + int(offset)
	case io.SeekEnd:
		abs = r.length + int(offset)
	default:
		return 0, errors.New("Seek: invalid whence")
	}

	if abs < 0 || abs > r.length {
		return 0, errors.New("Seek: invalid offset")
	}

	r.offset = abs
	return int64(abs), nil
}

func (r *Reader) Reset() {
	clear(r.bufs)
	r.bufs = nil
	r.length = 0
	r.offset = 0
}

func NewReader(buf ...[]byte) *Reader {
	b := &Reader{}
	for _, b1 := range buf {
		b.Append(b1)
	}
	return b
}
