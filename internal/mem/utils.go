package mem

import (
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/shirou/gopsutil/v4/mem"
)

var ErrNotEnoughMemory = errors.New("not enough memory")

// memoryReservation holds an owned available-memory counter used as a
// reservation budget across concurrent callers. It avoids atomic operations
// on the plain uint64 field of the external gopsutil struct.
type memoryReservation struct {
	available atomic.Uint64
}

func MemoryGrowCheck(growSize uint64) error {
	if conf.MinFreeMemory == 0 {
		return ErrNotEnoughMemory
	}
	r, err, _ := singleflight.AnyGroup.Do("MemoryGrowCheck", func() (any, error) {
		m, err := mem.VirtualMemory()
		if err != nil {
			return nil, err
		}
		if m.Available < conf.MinFreeMemory {
			return nil, ErrNotEnoughMemory
		}
		r := &memoryReservation{}
		r.available.Store(m.Available)
		return r, nil
	})
	if err != nil {
		return err
	}
	res := r.(*memoryReservation)
	for {
		available := res.available.Load()
		if available < growSize || available-growSize < conf.MinFreeMemory {
			return ErrNotEnoughMemory
		}
		if res.available.CompareAndSwap(available, available-growSize) {
			return nil
		}
	}
}

func NewGuardedMemory(cap, max uint64) (m LinearMemory, err error) {
	if err := MemoryGrowCheck(cap); err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", ErrNotEnoughMemory, r)
		}
	}()
	m, err = NewMemory(cap, max)
	if err != nil {
		return nil, err
	}
	if s, ok := m.(interface{ SetGrowCheck(GrowCheck) }); ok {
		s.SetGrowCheck(MemoryGrowCheck)
	}
	gm := &guardedMemory{LinearMemory: m}
	gm.cleanup = runtime.AddCleanup(gm, func(m LinearMemory) {
		m.Free()
	}, m)
	return gm, nil
}

type guardedMemory struct {
	LinearMemory
	cleanup runtime.Cleanup
}

func (s *guardedMemory) Reallocate(size uint64) (all []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", ErrNotEnoughMemory, r)
		}
	}()
	return s.LinearMemory.Reallocate(size)
}

func (s *guardedMemory) Free() error {
	s.cleanup.Stop()
	return s.LinearMemory.Free()
}
