package mem

import (
	"errors"
	"fmt"
	"runtime"
)

var ErrNotEnoughMemory = errors.New("not enough memory")

// NewManagedMemory creates memory with panic recovery and lifecycle cleanup.
func NewManagedMemory(cap, max uint64) (m LinearMemory, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", ErrNotEnoughMemory, r)
		}
	}()
	m, err = NewMemory(cap, max)
	if err != nil {
		return nil, err
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
