package stream

import (
	"context"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

// simRemote models a remote file on a high-latency link: every ReadAt is split
// into packet-sized requests, each paying the round trip latency, and at most
// concurrency of them are in flight.
type simRemote struct {
	size        int64
	latency     time.Duration
	bandwidth   int64 // bytes per second
	packet      int
	concurrency int
}

func (s *simRemote) ReadAt(p []byte, off int64) (int, error) {
	if off >= s.size {
		return 0, io.EOF
	}
	if remaining := s.size - off; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, s.concurrency)
	for n := 0; n < len(p); n += s.packet {
		end := min(n+s.packet, len(p))
		wg.Add(1)
		sem <- struct{}{}
		go func(n, end int) {
			defer wg.Done()
			defer func() { <-sem }()
			cost := s.latency + time.Duration(int64(end-n)*int64(time.Second)/s.bandwidth)
			time.Sleep(cost)
		}(n, end)
	}
	wg.Wait()
	return len(p), nil
}

// oldRangeReader is the previous behaviour: read the requested range directly,
// so every HTTP chunk becomes a remote read.
func oldRangeReader(file io.ReaderAt, size int64) RangeReaderFunc {
	return func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
		start, length := max(r.Start, 0), r.Length
		if size >= 0 {
			start = min(start, size)
			if length < 0 || length > size-start {
				length = size - start
			}
		}
		return &RateLimitReader{
			Ctx:     ctx,
			Limiter: ServerDownloadLimit,
			Reader:  io.NewSectionReader(file, start, length),
		}, nil
	}
}

func allowBuffering(tb testing.TB) {
	tb.Helper()
	previous := conf.MinFreeMemory
	conf.MinFreeMemory = 1
	tb.Cleanup(func() { conf.MinFreeMemory = previous })
}

// benchmarkDownload copies the whole file with the 64 KiB buffer used when
// serving a response.
func benchmarkDownload(b *testing.B, open func(io.ReaderAt, int64) RangeReaderFunc) {
	const total = 8 << 20
	allowBuffering(b)
	remote := &simRemote{
		size:        total,
		latency:     time.Millisecond,
		bandwidth:   200 << 20,
		packet:      32 << 10,
		concurrency: 64,
	}
	rr := open(remote, total)
	buf := make([]byte, 64<<10)
	b.SetBytes(total)
	b.ReportAllocs()
	for b.Loop() {
		rc, err := rr.RangeRead(context.Background(), http_range.Range{Length: -1})
		if err != nil {
			b.Fatal(err)
		}
		for {
			n, err := rc.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
			_ = n
		}
		if err := rc.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDownloadOld(b *testing.B) { benchmarkDownload(b, oldRangeReader) }

func BenchmarkDownloadNew(b *testing.B) { benchmarkDownload(b, NewBufferedRangeReader) }

// benchmarkConcurrentMemory keeps several ranges open and reports the heap they
// hold, which is what decides whether buffering can push a small host into OOM.
func benchmarkConcurrentMemory(b *testing.B, open func(io.ReaderAt, int64) RangeReaderFunc) {
	const (
		total   = 64 << 20
		ranges  = 16
		readLen = 4 << 10
	)
	allowBuffering(b)
	remote := &simRemote{size: total, packet: 32 << 10, concurrency: 64, bandwidth: 1 << 30}
	rr := open(remote, total)
	buf := make([]byte, readLen)
	b.ReportAllocs()
	for b.Loop() {
		// Two GCs also drop the sync.Pool victim cache, so the buffers held by
		// open ranges are newly allocated and therefore measurable.
		runtime.GC()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)

		opened := make([]io.ReadCloser, 0, ranges)
		for i := range ranges {
			rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: int64(i) * 1 << 20, Length: -1})
			if err != nil {
				b.Fatal(err)
			}
			if _, err := rc.Read(buf); err != nil {
				b.Fatal(err)
			}
			opened = append(opened, rc)
		}

		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/ranges/(1<<10), "heap-KiB/range")

		for _, rc := range opened {
			if err := rc.Close(); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkConcurrentMemoryOld(b *testing.B) { benchmarkConcurrentMemory(b, oldRangeReader) }

func BenchmarkConcurrentMemoryNew(b *testing.B) { benchmarkConcurrentMemory(b, NewBufferedRangeReader) }
