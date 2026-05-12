package cache

import (
	"errors"
	"io"
	"os"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/pkg/buffer"
)

type FileCache interface {
	buffer.Block
	io.Closer
	Truncate(size int64) error
}

type singleFileCache struct {
	*os.File
	size int64
}

func (s *singleFileCache) Size() int64 {
	return s.size
}

func (s *singleFileCache) Truncate(size int64) error {
	if size <= s.size {
		return nil
	}
	err := s.File.Truncate(size)
	if err == nil {
		s.size = size
	}
	return err
}

func (s *singleFileCache) Close() error {
	err := s.File.Close()
	_ = os.Remove(s.File.Name())
	return err
}

type fileBlock struct {
	file    *os.File
	size    int64
	written int64
}

type MultiFileCache struct {
	blocks []*fileBlock
	size   int64
}

func (s *MultiFileCache) Size() int64 {
	return s.size
}

func (m *MultiFileCache) Close() error {
	var errs []error
	for _, c := range m.blocks {
		if err := c.file.Close(); err != nil {
			errs = append(errs, err)
		}
		_ = os.Remove(c.file.Name())
	}
	clear(m.blocks)
	m.blocks = m.blocks[:0]
	return errors.Join(errs...)
}

func (m *MultiFileCache) Truncate(size int64) error {
	if size <= m.size {
		return nil
	}
	f, err := os.CreateTemp(conf.Conf.TempDir, "file-*")
	if err != nil {
		return err
	}
	m.blocks = append(m.blocks, &fileBlock{file: f, size: size - m.size})
	m.size = size
	return nil
}

func (m *MultiFileCache) ReadAt(p []byte, off int64) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off >= m.size {
		return 0, io.EOF
	}

	for _, c := range m.blocks {
		if off >= c.size {
			off -= c.size
			continue
		}

		canRead := min(len(p)-n, int(c.size-off))
		if canRead <= 0 {
			break
		}

		filled := 0

		if off < c.written {
			fileReadable := min(canRead, int(c.written-off))
			nn, fileErr := c.file.ReadAt(p[n:n+fileReadable], off)
			n += nn
			filled = nn
			if fileErr != nil && !errors.Is(fileErr, io.EOF) {
				return n, fileErr
			}
		}

		if n == len(p) {
			return n, nil
		}

		if zeroFill := canRead - filled; zeroFill > 0 {
			clear(p[n : n+zeroFill])
			n += zeroFill
		}

		if n == len(p) {
			return n, nil
		}
		off = 0
	}

	return n, io.EOF
}

func (m *MultiFileCache) WriteAt(p []byte, off int64) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off >= m.size {
		return 0, io.ErrShortWrite
	}

	for _, b := range m.blocks {
		if off >= b.size {
			off -= b.size
			continue
		}

		canWrite := min(len(p)-n, int(b.size-off))
		if canWrite <= 0 {
			break
		}

		nn, fileErr := b.file.WriteAt(p[n:n+canWrite], off)
		if end := off + int64(nn); end > b.written {
			b.written = end
		}
		n += nn
		if fileErr != nil {
			return n, fileErr
		}
		if nn < canWrite {
			return n, io.ErrShortWrite
		}
		if n == len(p) {
			return n, nil
		}
		off = 0
	}

	return n, io.ErrShortWrite
}

func NewFileCache(blockSize int64) (FileCache, error) {
	f, err := os.CreateTemp(conf.Conf.TempDir, "file-*")
	if err != nil {
		return nil, err
	}
	err = f.Truncate(blockSize)
	if err == nil {
		return &singleFileCache{File: f, size: blockSize}, nil
	}
	return &MultiFileCache{
		blocks: []*fileBlock{{file: f, size: blockSize}},
		size:   blockSize,
	}, nil
}
