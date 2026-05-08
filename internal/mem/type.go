package mem

type LinearMemory interface {
	Reallocate(size uint64) (all []byte, err error)
	Free() error
}
