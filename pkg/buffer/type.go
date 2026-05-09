package buffer

import "io"

type SectionWriter interface {
	io.WriterAt
	io.WriteSeeker
}

type SectionReader interface {
	io.ReaderAt
	io.ReadSeeker
}
