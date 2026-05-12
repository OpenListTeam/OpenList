package mem

import (
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

func NewMemory(cap, max uint64) (LinearMemory, error) {
	// Round up to the page size.
	rnd := uint64(windows.Getpagesize() - 1)
	res := (max + rnd) &^ rnd

	if res > math.MaxInt {
		// This ensures uintptr(res) overflows to a large value,
		// and windows.VirtualAlloc returns an error.
		res = math.MaxUint64
	}

	com := res
	kind := windows.MEM_COMMIT
	if cap < max { // Commit memory only if cap=max.
		com = 0
		kind = windows.MEM_RESERVE
	}

	// Reserve res bytes of address space, to ensure we won't need to move it.
	r, err := windows.VirtualAlloc(0, uintptr(res), uint32(kind), windows.PAGE_READWRITE)
	if err != nil {
		return nil, err
	}

	buf := unsafe.Slice((*byte)(unsafe.Pointer(r)), int(res))
	return &virtualMemory{addr: r, buf: buf[:com]}, nil
}

// The slice covers the entire mmapped memory:
//   - len(buf) is the already committed memory,
//   - cap(buf) is the reserved address space.
type virtualMemory struct {
	buf       []byte
	addr      uintptr
	growCheck GrowCheck
}

func (m *virtualMemory) SetGrowCheck(c GrowCheck) {
	m.growCheck = c
}

func (m *virtualMemory) Reallocate(size uint64) ([]byte, error) {
	com := uint64(len(m.buf))
	res := uint64(cap(m.buf))
	if com < size {
		if size <= res {
			// Grow geometrically, round up to the page size.
			rnd := uint64(windows.Getpagesize() - 1)
			new := com + com>>3
			new = min(max(size, new), res)
			new = (new + rnd) &^ rnd

			if m.growCheck != nil {
				if err := m.growCheck(new - com); err != nil {
					return nil, err
				}
			}

			// Commit additional memory up to new bytes.
			_, err := windows.VirtualAlloc(m.addr, uintptr(new), windows.MEM_COMMIT, windows.PAGE_READWRITE)
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

func (m *virtualMemory) Free() error {
	if m.addr != 0 {
		err := windows.VirtualFree(m.addr, 0, windows.MEM_RELEASE)
		if err != nil {
			return err
		}
		m.addr = 0
		m.buf = nil
	}
	return nil
}
