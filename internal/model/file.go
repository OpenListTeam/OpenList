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

// DirectUploadInfo contains the information needed for direct upload from client to storage
type DirectUploadInfo struct {
	UploadURL string `json:"upload_url"` // The URL to upload the file
	ChunkSize int64  `json:"chunk_size"` // The chunk size for uploading, 0 means no chunking required
	Headers   map[string]string `json:"headers,omitempty"` // Optional headers to include in the upload request
	Method    string `json:"method,omitempty"` // HTTP method, default is PUT
}
