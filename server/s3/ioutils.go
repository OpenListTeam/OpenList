// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import "io"

type noOpReadCloser struct{}

type readerWithCloser struct {
	io.Reader
	closer func() error
}

var _ io.ReadCloser = &readerWithCloser{}

func (d noOpReadCloser) Read(b []byte) (n int, err error) {
	return 0, io.EOF
}

func (d noOpReadCloser) Close() error {
	return nil
}

func limitReadCloser(rdr io.Reader, closer func() error, sz int64) io.ReadCloser {
	return &readerWithCloser{
		Reader: io.LimitReader(rdr, sz),
		closer: closer,
	}
}

// bodyErrorReader keeps the first error the request body raised while a caller
// consumed it. A caller that only sees the error the storage handed back cannot
// otherwise tell a stream that broke on the client side (a chunk frame that
// does not decode, a body that stops in the middle of a frame, a dropped
// connection) from the storage itself failing, and the two need different
// answers: 4xx for the request, 5xx for the gateway.
//
// The error is recorded, not returned instead of the original: the reader still
// behaves exactly like the one it wraps.
type bodyErrorReader struct {
	inner io.Reader
	err   error
}

var _ io.Reader = &bodyErrorReader{}

func (r *bodyErrorReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	// io.EOF is how a body that delivered everything it promised ends; any
	// other error means the stream itself broke.
	if err != nil && err != io.EOF && r.err == nil {
		r.err = err
	}
	return n, err
}

func (rwc *readerWithCloser) Close() error {
	if rwc.closer != nil {
		return rwc.closer()
	}
	return nil
}
