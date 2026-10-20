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
const windowSize = utils.MB

var windowPool = sync.Pool{New: func() any {
	return bufio.NewReaderSize(nil, windowSize)
}}

// NewBufferedRangeReader applies the server download limit using each range's context.
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
		// Check available memory and skip the extra buffer when memory is low.
		if length == 0 || mem.MemoryGrowCheck(windowSize) != nil {
			return reader, nil
		}
		buffer := windowPool.Get().(*bufio.Reader)
		buffer.Reset(reader.Reader)
		reader.Reader = buffer
		return utils.NewReadCloser(reader, sync.OnceValue(func() error {
			buffer.Reset(nil)
			windowPool.Put(buffer)
			return nil
		})), nil
	}
}
