package sftp

import (
	"context"
	"io"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// windowSize is how much data is fetched from the server per read ahead.
// An SFTP packet is limited to 32 KiB, so asking for less than that per round
// trip makes the throughput bound to the latency of the link. Reads larger
// than the packet size are split into concurrent requests by the sftp client.
const windowSize = utils.MB

var windowPool = sync.Pool{New: func() any { return make([]byte, windowSize) }}

// newRangeReader returns a range reader which reads ahead in windows of
// windowSize bytes. Every range gets its own window, so concurrent ranges may
// share the same sftp.File without touching its seek offset.
func newRangeReader(file io.ReaderAt, size int64) stream.RangeReaderFunc {
	return func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
		start, length := r.Start, r.Length
		if start < 0 {
			start = 0
		}
		if size >= 0 {
			if start > size {
				start = size
			}
			if length < 0 || start+length > size {
				length = size - start
			}
		}
		return &stream.RateLimitReader{
			Ctx:     ctx,
			Limiter: stream.ServerDownloadLimit,
			Reader: &rangeReader{
				file:      file,
				off:       start,
				remaining: length,
			},
		}, nil
	}
}

// rangeReader reads a byte range sequentially, but fills a whole window per
// ReadAt so the sftp client can keep several packets in flight.
type rangeReader struct {
	file      io.ReaderAt
	off       int64
	remaining int64 // bytes left in the range, -1 when unknown

	buf []byte // window buffer, taken from windowPool on first use
	pos int    // next byte of buf to return
	n   int    // number of valid bytes in buf
	err error  // sticky error reported once buf is drained
}

func (r *rangeReader) Read(p []byte) (int, error) {
	for r.pos == r.n {
		if r.err != nil {
			return 0, r.err
		}
		if len(p) == 0 {
			return 0, nil
		}
		if err := r.fill(); err != nil {
			r.err = err
			return 0, err
		}
	}
	n := copy(p, r.buf[r.pos:r.n])
	r.pos += n
	return n, nil
}

func (r *rangeReader) fill() error {
	if r.remaining == 0 {
		return io.EOF
	}
	if r.buf == nil {
		r.buf = windowPool.Get().([]byte)
	}
	want := len(r.buf)
	if r.remaining > 0 && r.remaining < int64(want) {
		want = int(r.remaining)
	}
	n, err := r.file.ReadAt(r.buf[:want], r.off)
	r.pos, r.n = 0, 0
	if n > 0 {
		r.off += int64(n)
		r.pos, r.n = 0, n
		if r.remaining > 0 {
			r.remaining -= int64(n)
		}
	}
	if err != nil {
		if n > 0 {
			// deliver the data read so far, report the error once it is consumed
			r.err = err
			return nil
		}
		return err
	}
	if n < want {
		// a short read without error means the range ends here
		r.remaining = 0
	}
	return nil
}

func (r *rangeReader) Close() error {
	if r.buf != nil {
		windowPool.Put(r.buf)
		r.buf = nil
	}
	r.pos, r.n = 0, 0
	return nil
}
