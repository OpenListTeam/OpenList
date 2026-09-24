package hybrid_cache

import (
	"fmt"
	"io"
	"sync"
)

type BufferStore struct {
	mu     sync.RWMutex
	blocks [][]byte
	size   int64
}

func (m *BufferStore) Size() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.size
}

func (m *BufferStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.blocks)
	m.blocks = nil
	m.size = 0
	return nil
}

func (m *BufferStore) ReadAt(p []byte, off int64) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off >= m.size {
		return 0, io.EOF
	}

	var n int
	for _, buf := range m.blocks {
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

func (m *BufferStore) WriteAt(p []byte, off int64) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off >= m.size {
		return 0, io.ErrShortWrite
	}

	var n int
	for _, b := range m.blocks {
		if off >= int64(len(b)) {
			off -= int64(len(b))
			continue
		}
		nn := copy(b[off:], p[n:])
		n += nn
		if n == len(p) {
			return n, nil
		}
		off = 0
	}

	return n, io.ErrShortWrite
}

func (m *BufferStore) GrowTo(size int64) (err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if size <= m.size {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered in %v", r)
		}
	}()
	m.blocks = append(m.blocks, make([]byte, size-m.size))
	m.size = size
	return nil
}

var _ BackingStore = (*BufferStore)(nil)
