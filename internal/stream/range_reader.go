package stream

import (
	"bufio"
	"context"
	"io"
	"math"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/mem"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// Large reads reduce network round trips and let clients pipeline requests.
const (
	windowSize         = utils.MB
	degradedWindowSize = 64 * utils.KB
)

var (
	windowPool = sync.Pool{New: func() any {
		return bufio.NewReaderSize(nil, windowSize)
	}}
	degradedPool = sync.Pool{New: func() any {
		return bufio.NewReaderSize(nil, degradedWindowSize)
	}}
)

// NewBufferedRangeReader buffers remote ranges and applies the server download
// limit using each range's context. Closing a range does not close the file.
func NewBufferedRangeReader(file io.ReaderAt, size int64) RangeReaderFunc {
	pool := &windowPool
	if mem.MemoryGrowCheck(windowSize) != nil {
		pool = &degradedPool
	}
	return newRangeReader(file, size, pool)
}

// Each range buffers independently and uses ReadAt without changing the shared file's seek offset.
func newRangeReader(file io.ReaderAt, size int64, pool *sync.Pool) RangeReaderFunc {
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
		reader := pool.Get().(*bufio.Reader)
		reader.Reset(io.NewSectionReader(file, start, length))
		return utils.NewReadCloser(&RateLimitReader{
			Ctx:     ctx,
			Limiter: ServerDownloadLimit,
			Reader:  reader,
		}, sync.OnceValue(func() error {
			reader.Reset(nil)
			pool.Put(reader)
			return nil
		})), nil
	}
}
