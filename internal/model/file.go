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

// FileRangeReader 是对 RangeReaderIF 的轻量包装，表明由 RangeReaderIF.RangeRead
// 返回的 io.ReadCloser 同时实现了 model.File（即支持 Read/ReadAt/Seek）。
// 只有满足这些才需要使用 FileRangeReader，否则直接使用 RangeReaderIF 即可。
type FileRangeReader struct {
	RangeReaderIF
}
