package buffer

import "io"

type WriteAtSeekerProvider interface{ GetWriteAtSeeker() WriteAtSeeker }

func WriteAtSeekerOf(b Block) WriteAtSeeker {
	if p, ok := b.(WriteAtSeekerProvider); ok {
		return p.GetWriteAtSeeker()
	}
	return io.NewOffsetWriter(b, 0)
}

type ReadAtSeekerProvider interface{ GetReadAtSeeker() ReadAtSeeker }

func ReadAtSeekerOf(b Block) ReadAtSeeker {
	if p, ok := b.(ReadAtSeekerProvider); ok {
		return p.GetReadAtSeeker()
	}
	return io.NewSectionReader(b, 0, b.Size())
}
