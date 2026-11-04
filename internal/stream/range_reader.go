package stream

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"math"
	"sync"
	"sync/atomic"

	"github.com/OpenListTeam/OpenList/v4/internal/mem"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

// windowSize is the prefetch size. It is the granularity at which the output
// stream turns into remote round trips, and the amount of memory one in-flight
// range holds: larger windows amortize latency, but cost more per connection.
const windowSize = utils.MB

// emptyReader keeps pooled buffers valid instead of holding a nil reader.
var emptyReader = bytes.NewReader(nil)

var windowPool = sync.Pool{New: func() any {
	return bufio.NewReaderSize(emptyReader, windowSize)
}}

// fallbackLog counts degraded reads so the warning cannot flood the log.
var fallbackLog atomic.Uint64

func logFallback(err error) {
	if n := fallbackLog.Add(1); n <= 3 || n%1000 == 0 {
		log.WithError(err).Warnf("range reader falling back to unbuffered reads (count: %d)", n)
	}
}

type closedReader struct{}

func (closedReader) Read([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

// NewBufferedRangeReader applies the server download limit using each range's
// context. It must stay a value type: SFTP and SMB put the result into a Link
// that may be cloned, and the returned reader is cheap to create per request.
func NewBufferedRangeReader(file io.ReaderAt, size int64) RangeReaderFunc {
	return func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
		start, length := max(r.Start, 0), r.Length
		if size >= 0 {
			start = min(start, size)
			if length < 0 || length > size-start {
				length = size - start
			}
		} else if length < 0 {
			length = math.MaxInt64 - start
		}
		reader := &RateLimitReader{
			Ctx:     ctx,
			Limiter: ServerDownloadLimit,
			Reader:  io.NewSectionReader(file, start, length),
		}
		if length == 0 {
			return reader, nil
		}
		// Prefetching is best effort: fall back to unbuffered reads when the
		// system is low on memory. min_free_memory <= 0 disables this path by
		// design, so "no buffering" also means the admin asked for it.
		if err := mem.MemoryGrowCheck(windowSize); err != nil {
			logFallback(err)
			return reader, nil
		}
		buffer := windowPool.Get().(*bufio.Reader)
		buffer.Reset(reader.Reader)
		reader.Reader = buffer
		return utils.NewReadCloser(reader, sync.OnceValue(func() error {
			reader.Reader = closedReader{}
			buffer.Reset(emptyReader)
			windowPool.Put(buffer)
			return nil
		})), nil
	}
}
