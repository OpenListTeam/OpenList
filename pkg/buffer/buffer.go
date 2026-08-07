package buffer

import (
	"io"
)

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
