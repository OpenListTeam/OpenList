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

func MemoryGrowCheck(growSize uint64) error {
	m, err, _ := singleflight.AnyGroup.Do("SafeMemory.GrowLimit", func() (any, error) {
		m, err := mem.VirtualMemory()
		if err != nil {
			return nil, err
		}
		if m.Available < conf.MinFreeMemory {
			return nil, ErrNotEnoughMemory
		}
		return m, nil
	})
	if err != nil {
		return err
	}
	memStat := m.(*mem.VirtualMemoryStat)
	for {
		available := atomic.LoadUint64(&memStat.Available)
		if available-growSize < conf.MinFreeMemory {
			return ErrNotEnoughMemory
		}
		if atomic.CompareAndSwapUint64(&memStat.Available, available, available-growSize) {
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
	runtime.SetFinalizer(m, func(m LinearMemory) {
		m.Free()
	})
	if s, ok := m.(interface{ SetGrowCheck(GrowCheck) }); ok {
		s.SetGrowCheck(MemoryGrowCheck)
	}
	return &guardedMemory{m}, nil
}

type guardedMemory struct {
	LinearMemory
}

func (s *guardedMemory) Reallocate(size uint64) (all []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", ErrNotEnoughMemory, r)
		}
	}()
	return s.LinearMemory.Reallocate(size)
}
