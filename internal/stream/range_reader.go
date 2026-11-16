package stream

import (
	"bufio"
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

const windowSize = utils.MB

var windowPool = sync.Pool{New: func() any {
	return bufio.NewReaderSize(nil, windowSize)
}}

var fallbackLog atomic.Uint64

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
		if err := mem.MemoryGrowCheck(windowSize); err != nil {
			if n := fallbackLog.Add(1); n <= 3 || n%1000 == 0 {
				log.WithError(err).Warnf("range reader falling back to unbuffered reads (count: %d)", n)
			}
			return reader, nil
		}
		buffer := windowPool.Get().(*bufio.Reader)
		buffer.Reset(reader)
		return &RateLimitReader{
			Ctx:    ctx,
			Reader: &bufferedRangeReader{buffer: buffer},
		}, nil
	}
}

// Each range owns its reader; callers close it after reading has stopped.
type bufferedRangeReader struct {
	buffer *bufio.Reader
}

func (r *bufferedRangeReader) Read(p []byte) (int, error) {
	if r.buffer == nil {
		return 0, io.ErrClosedPipe
	}
	return r.buffer.Read(p)
}

func (r *bufferedRangeReader) Close() error {
	if r.buffer != nil {
		r.buffer.Reset(nil)
		windowPool.Put(r.buffer)
		r.buffer = nil
	}
	return nil
}
