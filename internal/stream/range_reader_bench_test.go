package stream_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

// latentReaderAt models a remote round trip: every ReadAt pays one latency.
// It is a per-request cost model for comparing the two reader paths, not a
// link emulation, so the throughput numbers are not a capacity estimate.
type latentReaderAt struct {
	data    []byte
	latency time.Duration
}

func (l *latentReaderAt) ReadAt(p []byte, off int64) (int, error) {
	time.Sleep(l.latency)
	if off >= int64(len(l.data)) {
		return 0, io.EOF
	}
	n := copy(p, l.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// discardWriter avoids io.Discard's ReadFrom, which would impose its own 8KiB
// copy buffer and hide the read size we want to measure.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Both benchmarks read 4MB through a 64KiB output buffer, i.e. the way the HTTP
// handler copies a response body.
func benchmarkRangeReader(b *testing.B, minFreeMemory uint64) {
	prev := conf.MinFreeMemory
	conf.MinFreeMemory = minFreeMemory
	defer func() { conf.MinFreeMemory = prev }()

	data := seqData(4 << 20)
	rr := stream.NewBufferedRangeReader(&latentReaderAt{data: data, latency: time.Millisecond}, int64(len(data)))
	buf := make([]byte, 64<<10)
	b.SetBytes(int64(len(data)))

	for b.Loop() {
		rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: 0, Length: -1})
		if err != nil {
			b.Fatal(err)
		}
		if _, err = io.CopyBuffer(discardWriter{}, rc, buf); err != nil {
			b.Fatal(err)
		}
		if err = rc.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRangeReaderBuffered(b *testing.B)   { benchmarkRangeReader(b, 1) }
func BenchmarkRangeReaderUnbuffered(b *testing.B) { benchmarkRangeReader(b, 0) }
