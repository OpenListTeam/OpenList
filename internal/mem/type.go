package mem

type LinearMemory interface {
	// 线程不安全
	Reallocate(size uint64) (all []byte, err error)
	Free() error
}

type GrowCheck func(growSize uint64) error
