//go:build unix

package alloc // import "github.com/ncruces/go-sqlite3/internal/alloc"

import (
	"math"

	"github.com/tetratelabs/wazero/experimental"
	"golang.org/x/sys/unix"
)

func NewMemory(cap, max uint64) experimental.LinearMemory {
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
		panic(err)
	}
	return &mmappedMemory{buf: b[:com]}
}

// The slice covers the entire mmapped memory:
//   - len(buf) is the already committed memory,
//   - cap(buf) is the reserved address space.
type mmappedMemory struct {
	buf []byte
}

func (m *mmappedMemory) Reallocate(size uint64) []byte {
	com := uint64(len(m.buf))
	res := uint64(cap(m.buf))
	if com < size && size <= res {
		// Grow geometrically, round up to the page size.
		rnd := uint64(unix.Getpagesize() - 1)
		new := com + com>>3
		new = min(max(size, new), res)
		new = (new + rnd) &^ rnd

		// Commit additional memory up to new bytes.
		err := unix.Mprotect(m.buf[com:new], unix.PROT_READ|unix.PROT_WRITE)
		if err != nil {
			return nil
		}

		m.buf = m.buf[:new] // Update committed memory.
	}
	// Limit returned capacity because bytes beyond
	// len(m.buf) have not yet been committed.
	return m.buf[:size:len(m.buf)]
}

func (m *mmappedMemory) Free() {
	err := unix.Munmap(m.buf[:cap(m.buf)])
	if err != nil {
		panic(err)
	}
	m.buf = nil
}
