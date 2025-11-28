package alloc // import "github.com/ncruces/go-sqlite3/internal/alloc"

import (
	"math"
	"unsafe"

	"github.com/tetratelabs/wazero/experimental"
	"golang.org/x/sys/windows"
)

func NewMemory(cap, max uint64) experimental.LinearMemory {
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
		panic(err)
	}
	buf := unsafe.Slice((*byte)(unsafe.Pointer(r)), int(max))
	mem := virtualMemory{addr: r, buf: buf[:com:res]}
	return &mem
}

// The slice covers the entire mmapped memory:
//   - len(buf) is the already committed memory,
//   - cap(buf) is the reserved address space.
type virtualMemory struct {
	buf  []byte
	addr uintptr
}

func (m *virtualMemory) Reallocate(size uint64) []byte {
	com := uint64(len(m.buf))
	res := uint64(cap(m.buf))
	if com < size && size <= res {
		// Grow geometrically, round up to the page size.
		rnd := uint64(windows.Getpagesize() - 1)
		new := com + com>>3
		new = min(max(size, new), res)
		new = (new + rnd) &^ rnd

		// Commit additional memory up to new bytes.
		_, err := windows.VirtualAlloc(m.addr, uintptr(new), windows.MEM_COMMIT, windows.PAGE_READWRITE)
		if err != nil {
			return nil
		}

		m.buf = m.buf[:new] // Update committed memory.
	}
	// Limit returned capacity because bytes beyond
	// len(m.buf) have not yet been committed.
	return m.buf[:size:len(m.buf)]
}

func (m *virtualMemory) Free() {
	err := windows.VirtualFree(m.addr, 0, windows.MEM_RELEASE)
	if err != nil {
		panic(err)
	}
	m.addr = 0
}
