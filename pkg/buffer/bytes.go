package buffer

import (
	"errors"
	"io"
)

// Bytes用于存储不复用的[]byte
type Bytes struct {
	bufs   [][]byte
	length int
	offset int
}

func (b *Bytes) Len() int {
	return b.length
}

func (b *Bytes) Append(buf []byte) {
	b.length += len(buf)
	b.bufs = append(b.bufs, buf)
}

func (b *Bytes) Read(p []byte) (int, error) {
	n, err := b.ReadAt(p, int64(b.offset))
	if n > 0 {
		b.offset += n
	}
	return n, err
}

func (b *Bytes) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(b.length) {
		return 0, io.EOF
	}

	n, length := 0, int64(0)
	readFrom := false
	for _, buf := range b.bufs {
		newLength := length + int64(len(buf))
		if readFrom {
			w := copy(p[n:], buf)
			n += w
		} else if off < newLength {
			readFrom = true
			bufOffset := int(off - length)
			w := copy(p[n:], buf[bufOffset:])
			n += w
		}
		if n == len(p) {
			return n, nil
		}
		length = newLength
	}

	return n, io.EOF
}

func (b *Bytes) Seek(offset int64, whence int) (int64, error) {
	var abs int
	switch whence {
	case io.SeekStart:
		abs = int(offset)
	case io.SeekCurrent:
		abs = b.offset + int(offset)
	case io.SeekEnd:
		abs = b.length + int(offset)
	default:
		return 0, errors.New("Seek: invalid whence")
	}

	if abs < 0 {
		return 0, errors.New("Seek: invalid offset")
	}

	b.offset = abs
	return int64(abs), nil
}

func (b *Bytes) Reset() {
	clear(b.bufs)
	b.bufs = nil
	b.length = 0
	b.offset = 0
}

func NewBytes(buf ...[]byte) *Bytes {
	b := &Bytes{}
	for _, b1 := range buf {
		b.Append(b1)
	}
	return b
}
