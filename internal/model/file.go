package model

import (
	"errors"
	"io"
)

// File is basic file level accessing interface
type File interface {
	io.Reader
	io.ReaderAt
	io.Seeker
}
type FileCloser struct {
	File
	io.Closer
}

func (f *FileCloser) Close() error {
	var errs []error
	if clr, ok := f.File.(io.Closer); ok {
		errs = append(errs, clr.Close())
	}
	if f.Closer != nil {
		errs = append(errs, f.Closer.Close())
	}
	return errors.Join(errs...)
}

type FileRangeReader struct {
	RangeReaderIF
}
