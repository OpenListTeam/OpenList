package buffer

import (
	"errors"
	"io"
	"os"
)

type PeekFile struct {
	peek   *Reader
	file   *os.File
	offset int64
	size   int64
}

func (p *PeekFile) Read(b []byte) (n int, err error) {
	n, err = p.ReadAt(b, p.offset)
	if n > 0 {
		p.offset += int64(n)
	}
	return n, err
}

func (p *PeekFile) ReadAt(b []byte, off int64) (n int, err error) {
	if off < p.peek.Size() {
		n, err = p.peek.ReadAt(b, off)
		if err == nil || n == len(b) {
			return n, nil
		}
		// EOF
	}
	var nn int
	nn, err = p.file.ReadAt(b[n:], off+int64(n)-p.peek.Size())
	return n + nn, err
}

func (p *PeekFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		if offset == 0 {
			return p.offset, nil
		}
		offset = p.offset + offset
	case io.SeekEnd:
		offset = p.size + offset
	default:
		return 0, errors.New("Seek: invalid whence")
	}

	if offset < 0 || offset > p.size {
		return 0, errors.New("Seek: invalid offset")
	}
	if offset <= p.peek.Size() {
		_, err := p.peek.Seek(offset, io.SeekStart)
		if err != nil {
			return 0, err
		}
		_, err = p.file.Seek(0, io.SeekStart)
		if err != nil {
			return 0, err
		}
	} else {
		_, err := p.peek.Seek(p.peek.Size(), io.SeekStart)
		if err != nil {
			return 0, err
		}
		_, err = p.file.Seek(offset-p.peek.Size(), io.SeekStart)
		if err != nil {
			return 0, err
		}
	}

	p.offset = offset
	return offset, nil
}

func (p *PeekFile) Size() int64 {
	return p.size
}

func NewPeekFile(peek *Reader, file *os.File) (*PeekFile, error) {
	stat, err := file.Stat()
	if err == nil {
		return &PeekFile{peek: peek, file: file, size: stat.Size() + peek.Size()}, nil
	}
	return nil, err
}
