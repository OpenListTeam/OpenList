//go:build unix

package mem

import (
	"math"

	"golang.org/x/sys/unix"
)

func NewMemory(cap, max uint64) (LinearMemory, error) {
	// Round up to the page size.
	rnd := uint64(unix.Getpagesize() - 1)
	res := (max + rnd) &^ rnd

	if res > math.MaxInt {
		// This ensures int(res) overflows to a negative value,
		// and unix.Mmap returns EINVAL.
		res = math.MaxUint64
	}

	com := res
	prot := unix.PROT_READ | unix.PROT_WRITE
	if cap < max { // Commit memory only if cap=max.
		com = 0
		prot = unix.PROT_NONE
	}

	// Reserve res bytes of address space, to ensure we won't need to move it.
	// A protected, private, anonymous mapping should not commit memory.
	b, err := unix.Mmap(-1, 0, int(res), prot, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		return nil, err
	}
	return &mmappedMemory{buf: b[:com]}, nil
}

// The slice covers the entire mmapped memory:
//   - len(buf) is the already committed memory,
//   - cap(buf) is the reserved address space.
type mmappedMemory struct {
	buf       []byte
	growCheck GrowCheck
}

func (m *mmappedMemory) SetGrowCheck(c GrowCheck) {
	m.growCheck = c
}

func (m *mmappedMemory) Reallocate(size uint64) ([]byte, error) {
	com := uint64(len(m.buf))
	res := uint64(cap(m.buf))
	if com < size {
		if size <= res {
			// Grow geometrically, round up to the page size.
			rnd := uint64(unix.Getpagesize() - 1)
			new := com + com>>3
			new = min(max(size, new), res)
			new = (new + rnd) &^ rnd

			if m.growCheck != nil {
				if err := m.growCheck(new - com); err != nil {
					return nil, err
				}
			}

			// Commit additional memory up to new bytes.
			err := unix.Mprotect(m.buf[com:new], unix.PROT_READ|unix.PROT_WRITE)
			if err != nil {
				return nil, err
			}

			m.buf = m.buf[:new] // Update committed memory.
		} else {
			return nil, ErrNotEnoughMemory
		}
	}
	// Limit returned capacity because bytes beyond
	// len(m.buf) have not yet been committed.
	return m.buf[:size:len(m.buf)], nil
}

func (m *mmappedMemory) Free() error {
	if m.buf != nil {
		err := unix.Munmap(m.buf[:cap(m.buf)])
		if err != nil {
			return err
		}
		m.buf = nil
	}
	return nil
}
