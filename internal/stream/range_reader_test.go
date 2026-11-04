package stream_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/mem"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

// windowSize mirrors the buffered range reader window (utils.MB).
const windowSize = 1024 * 1024

// seqData is position dependent, so a wrong offset or a recycled buffer shows
// up as a content mismatch instead of passing silently.
func seqData(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i*31 + i>>13)
	}
	return data
}

// countingReaderAt counts upstream range requests, which is how we tell the
// buffered path (one call per window) from the unbuffered fallback.
type countingReaderAt struct {
	data []byte
	gets atomic.Int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.gets.Add(1)
	if off >= int64(len(c.data)) {
		return 0, io.EOF
	}
	n := copy(p, c.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func setMinFreeMemory(t *testing.T, value uint64) {
	t.Helper()
	prev := conf.MinFreeMemory
	t.Cleanup(func() { conf.MinFreeMemory = prev })
	conf.MinFreeMemory = value
}

// requireBuffered skips when the machine is too low on memory to ever take the
// buffered path, so the test does not fail on an unrelated precondition.
func requireBuffered(t *testing.T) {
	t.Helper()
	if err := mem.MemoryGrowCheck(windowSize); err != nil {
		t.Skipf("buffered range reader disabled here: %v", err)
	}
}

func drain(t *testing.T, rc io.Reader, chunk int) []byte {
	t.Helper()
	var out bytes.Buffer
	p := make([]byte, chunk)
	for {
		n, err := rc.Read(p)
		out.Write(p[:n])
		if errors.Is(err, io.EOF) {
			return out.Bytes()
		}
		if err != nil {
			t.Fatalf("read error = %v", err)
		}
	}
}

// A download must cost one upstream request per window, not one per output read.
func TestRangeReaderPrefetchesWindows(t *testing.T) {
	requireBuffered(t)
	data := seqData(8 * windowSize)
	src := &countingReaderAt{data: data}
	rr := stream.NewBufferedRangeReader(src, int64(len(data)))

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: 0, Length: -1})
	if err != nil {
		t.Fatalf("RangeRead() error = %v", err)
	}
	defer rc.Close()

	if got := drain(t, rc, 4<<10); !bytes.Equal(got, data) {
		t.Fatalf("content mismatch: %d bytes, want %d", len(got), len(data))
	}
	// Unbuffered would be len(data)/(4<<10) = 2048 calls.
	if gets := src.gets.Load(); gets > 12 {
		t.Fatalf("upstream ReadAt calls = %d, want <= 12", gets)
	}
}

func TestRangeReaderRangeMath(t *testing.T) {
	data := seqData(3 * windowSize)
	size := int64(len(data))
	rr := stream.NewBufferedRangeReader(bytes.NewReader(data), size)

	for _, tt := range []struct {
		name string
		r    http_range.Range
		want []byte
	}{
		{"bounded", http_range.Range{Start: 1024, Length: 4096}, data[1024 : 1024+4096]},
		{"crosses-window", http_range.Range{Start: windowSize - 100, Length: 500}, data[windowSize-100 : windowSize+400]},
		{"length-minus-one", http_range.Range{Start: size - 10, Length: -1}, data[size-10:]},
		{"length-overflow", http_range.Range{Start: size - 10, Length: 100}, data[size-10:]},
		{"negative-start", http_range.Range{Start: -5, Length: 10}, data[:10]},
		{"start-at-eof", http_range.Range{Start: size, Length: 100}, nil},
		{"start-beyond-eof", http_range.Range{Start: size + 100, Length: 100}, nil},
		{"zero-length", http_range.Range{Start: 0, Length: 0}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rc, err := rr.RangeRead(context.Background(), tt.r)
			if err != nil {
				t.Fatalf("RangeRead(%+v) error = %v", tt.r, err)
			}
			defer rc.Close()
			if got := drain(t, rc, 8<<10); !bytes.Equal(got, tt.want) {
				t.Fatalf("RangeRead(%+v) = %d bytes, want %d", tt.r, len(got), len(tt.want))
			}
		})
	}
}

// Unknown remote size used to produce a negative length, hence an empty body.
func TestRangeReaderUnknownSize(t *testing.T) {
	requireBuffered(t)
	data := seqData(2*windowSize + 123)
	rr := stream.NewBufferedRangeReader(bytes.NewReader(data), -1)

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: 0, Length: -1})
	if err != nil {
		t.Fatalf("RangeRead() error = %v", err)
	}
	defer rc.Close()

	if got := drain(t, rc, 8<<10); !bytes.Equal(got, data) {
		t.Fatalf("unknown-size read = %d bytes, want %d", len(got), len(data))
	}
}

// Ranges from one RangeReaderFunc run concurrently, each with its own window;
// a recycled buffer would surface as foreign content here.
func TestRangeReaderConcurrentRanges(t *testing.T) {
	requireBuffered(t)
	const rangeSize = 2*windowSize + 4096
	data := seqData(6 * rangeSize)
	rr := stream.NewBufferedRangeReader(bytes.NewReader(data), int64(len(data)))

	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := int64(i * rangeSize)
			rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: start, Length: rangeSize})
			if err != nil {
				errs <- err
				return
			}
			defer rc.Close()
			got := make([]byte, 0, rangeSize)
			p := make([]byte, 8<<10)
			for {
				n, err := rc.Read(p)
				got = append(got, p[:n]...)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					errs <- err
					return
				}
			}
			if !bytes.Equal(got, data[start:start+rangeSize]) {
				errs <- errors.New("range content mismatch")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent RangeRead error = %v", err)
	}
}

func TestRangeReaderCloseIsIdempotent(t *testing.T) {
	requireBuffered(t)
	data := seqData(windowSize + 1)
	rr := stream.NewBufferedRangeReader(bytes.NewReader(data), int64(len(data)))

	for i := range 8 {
		rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: 0, Length: -1})
		if err != nil {
			t.Fatalf("iteration %d: RangeRead() error = %v", i, err)
		}
		if _, err := rc.Read(make([]byte, 1024)); err != nil {
			t.Fatalf("iteration %d: Read() error = %v", i, err)
		}
		if err := errors.Join(rc.Close(), rc.Close()); err != nil {
			t.Fatalf("iteration %d: Close() error = %v", i, err)
		}
		if _, err := rc.Read(make([]byte, 1024)); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("iteration %d: Read() after Close = %v, want %v", i, err, io.ErrClosedPipe)
		}
	}
}

// Buffering off must still serve identical content.
func TestRangeReaderLowMemoryFallback(t *testing.T) {
	setMinFreeMemory(t, 0)
	data := seqData(2 * windowSize)
	src := &countingReaderAt{data: data}
	rr := stream.NewBufferedRangeReader(src, int64(len(data)))

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: 0, Length: -1})
	if err != nil {
		t.Fatalf("RangeRead() error = %v", err)
	}
	defer rc.Close()

	if got := drain(t, rc, 4<<10); !bytes.Equal(got, data) {
		t.Fatalf("fallback read = %d bytes, want %d", len(got), len(data))
	}
	if gets := src.gets.Load(); gets < int64(len(data)/(4<<10))/2 {
		t.Fatalf("upstream ReadAt calls = %d, want >= %d (buffering should be off)", gets, len(data)/(4<<10)/2)
	}
}

// Wired the way the SFTP/SMB drivers do it: Link -> SeekableStream, which now
// opens the remote range lazily on first read.
func TestRangeReaderThroughLink(t *testing.T) {
	requireBuffered(t)
	data := seqData(3*windowSize + 7)
	src := &countingReaderAt{data: data}

	ss, err := stream.NewSeekableStream(&stream.FileStream{
		Obj: &model.Object{Size: int64(len(data))},
		Ctx: context.Background(),
	}, &model.Link{
		RangeReader:   stream.NewBufferedRangeReader(src, int64(len(data))),
		ContentLength: int64(len(data)),
	})
	if err != nil {
		t.Fatalf("NewSeekableStream() error = %v", err)
	}
	got, err := io.ReadAll(ss)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("streamed %d bytes, want %d", len(got), len(data))
	}
	if gets := src.gets.Load(); gets > 6 {
		t.Fatalf("upstream ReadAt calls = %d, want <= 6", gets)
	}
	if err := ss.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
