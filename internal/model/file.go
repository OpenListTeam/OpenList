package model

import "io"

// File is basic file level accessing interface
type File interface {
	io.Reader
	io.ReaderAt
	io.Seeker
}
