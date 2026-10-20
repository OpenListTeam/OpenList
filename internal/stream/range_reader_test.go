package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// countingReaderAt records every ReadAt so tests can assert how much data is
// requested per round trip.
type countingReaderAt struct {
	mu    sync.Mutex
	data  []byte
	reads []readOp
}

type readOp struct {
	off int64
	n   int
}

func (f *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	f.reads = append(f.reads, readOp{off, len(p)})
	f.mu.Unlock()
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *countingReaderAt) ops() []readOp {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]readOp(nil), f.reads...)
}

// shortReaderAt simulates a file ending before its advertised size.
type shortReaderAt struct {
	countingReaderAt
}

func (f *shortReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, _ := f.countingReaderAt.ReadAt(p[:len(p)/2], off)
	return n, io.EOF
}

func testData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*7 + i/251)
	}
	return data
}

// drain reads rc with a fixed chunk size, mimicking how the http server copies
// a response (64 KiB buffer).
func drain(t *testing.T, rc io.ReadCloser, chunk int) []byte {
	t.Helper()
	defer rc.Close()
	var out bytes.Buffer
	buf := make([]byte, chunk)
	for {
		n, err := rc.Read(buf)
		out.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			return out.Bytes()
		}
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}
	}
}

func setMinFreeMemory(t *testing.T, value uint64) {
	t.Helper()
	previous := conf.MinFreeMemory
	t.Cleanup(func() { conf.MinFreeMemory = previous })
	conf.MinFreeMemory = value
}

// bufferedRangeReader returns a range reader which is allowed to buffer.
func bufferedRangeReader(t *testing.T, file io.ReaderAt, size int64) RangeReaderFunc {
	t.Helper()
	setMinFreeMemory(t, 1)
	return NewBufferedRangeReader(file, size)
}

func TestRangeReaderReadsWholeWindows(t *testing.T) {
	data := testData(3*windowSize + 1234)
	f := &countingReaderAt{data: data}
	rr := bufferedRangeReader(t, f, int64(len(data)))

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, rc, 64*1024); !bytes.Equal(got, data) {
		t.Fatal("content mismatch")
	}

	ops := f.ops()
	if len(ops) != 4 {
		t.Fatalf("expected 4 ReadAt calls for size %d, got %d: %v", len(data), len(ops), ops)
	}
	if ops[0].n != windowSize {
		t.Fatalf("expected first read to fill a whole window (%d), got %d", windowSize, ops[0].n)
	}
	if last := ops[len(ops)-1]; last.n != 1234 || last.off != int64(3*windowSize) {
		t.Fatalf("unexpected tail read: %+v", last)
	}
}

func TestRangeReaderHonoursRange(t *testing.T) {
	data := testData(2 * windowSize)
	f := &countingReaderAt{data: data}
	rr := bufferedRangeReader(t, f, int64(len(data)))

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: 1234, Length: 1000})
	if err != nil {
		t.Fatal(err)
	}
	want := data[1234:2234]
	if got := drain(t, rc, 100); !bytes.Equal(got, want) {
		t.Fatal("content mismatch")
	}
	if ops := f.ops(); len(ops) != 1 || ops[0].off != 1234 || ops[0].n != 1000 {
		t.Fatalf("expected a single clamped read, got %v", ops)
	}
}

func TestRangeReaderClampsToSize(t *testing.T) {
	data := testData(100)
	f := &countingReaderAt{data: data}
	rr := bufferedRangeReader(t, f, int64(len(data)))

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: 80, Length: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, rc, 64); !bytes.Equal(got, data[80:]) {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestRangeReaderAtEndIsEmpty(t *testing.T) {
	data := testData(100)
	f := &countingReaderAt{data: data}
	rr := bufferedRangeReader(t, f, int64(len(data)))

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: 100, Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, rc, 64); len(got) != 0 {
		t.Fatalf("expected empty read, got %d bytes", len(got))
	}
	if ops := f.ops(); len(ops) != 0 {
		t.Fatalf("expected no server read, got %v", ops)
	}
}

func TestRangeReaderUnknownSize(t *testing.T) {
	data := testData(windowSize + 10)
	f := &countingReaderAt{data: data}
	rr := bufferedRangeReader(t, f, -1)

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, rc, 64*1024); !bytes.Equal(got, data) {
		t.Fatal("content mismatch")
	}
}

func TestRangeReaderShortReadEndsRange(t *testing.T) {
	data := testData(windowSize)
	f := &shortReaderAt{countingReaderAt{data: data}}
	rr := bufferedRangeReader(t, f, int64(len(data)))

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, rc, 64*1024)
	if len(got) != windowSize/2 {
		t.Fatalf("expected the short read to end the range, got %d bytes", len(got))
	}
	if ops := f.ops(); len(ops) != 1 {
		t.Fatalf("expected no retry after a short read, got %v", ops)
	}
}

func TestRangeReaderContextCancel(t *testing.T) {
	data := testData(windowSize)
	f := &countingReaderAt{data: data}
	rr := bufferedRangeReader(t, f, int64(len(data)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rc, err := rr.RangeRead(ctx, http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if _, err := rc.Read(make([]byte, 1024)); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if ops := f.ops(); len(ops) != 0 {
		t.Fatalf("canceled range performed reads: %v", ops)
	}
	other, err := rr.RangeRead(context.Background(), http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, other, 64*1024); !bytes.Equal(got, data) {
		t.Fatal("canceling one range affected another")
	}
}

func TestRangeReaderConcurrentRanges(t *testing.T) {
	data := testData(6 * windowSize)
	f := &countingReaderAt{data: data}
	rr := bufferedRangeReader(t, f, int64(len(data)))

	const ranges = 8
	var wg sync.WaitGroup
	errs := make(chan error, ranges)
	for i := range ranges {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := int64(i * windowSize / 2)
			length := int64(windowSize)
			rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: start, Length: length})
			if err != nil {
				errs <- err
				return
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, data[start:start+length]) {
				errs <- fmt.Errorf("range %d..%d mismatch", start, start+length)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestRangeReaderCloseOnce(t *testing.T) {
	data := testData(100)
	rr := bufferedRangeReader(t, bytes.NewReader(data), int64(len(data)))
	first, err := rr.RangeRead(context.Background(), http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := rr.RangeRead(context.Background(), http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if got := drain(t, second, 16); !bytes.Equal(got, data) {
		t.Fatal("closing the first range again affected the reused buffer")
	}
}

func TestRangeReaderLargeRead(t *testing.T) {
	data := testData(2*windowSize + 1234)
	f := &countingReaderAt{data: data}
	rr := bufferedRangeReader(t, f, int64(len(data)))

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, rc, 2*windowSize); !bytes.Equal(got, data) {
		t.Fatal("content mismatch")
	}
	if ops := f.ops(); len(ops) != 2 || ops[0].n != 2*windowSize || ops[1].n != 1234 {
		t.Fatalf("expected direct reads into the caller's buffer, got %v", ops)
	}
}

func TestBufferedRangeReaderLowMemory(t *testing.T) {
	setMinFreeMemory(t, 0)
	data := []byte("0123456789")

	for _, size := range []int64{int64(len(data)), -1} {
		rr := NewBufferedRangeReader(bytes.NewReader(data), size)
		rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: 3, Length: -1})
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		limited, ok := rc.(*RateLimitReader)
		if !ok {
			t.Fatalf("expected unbuffered rate-limited reader, got %T", rc)
		}
		if _, ok := limited.Reader.(*io.SectionReader); !ok {
			t.Fatalf("expected direct section reader, got %T", limited.Reader)
		}
		got, err := io.ReadAll(rc)
		if err != nil || !bytes.Equal(got, data[3:]) {
			t.Fatalf("read = %q, %v; want %q", got, err, data[3:])
		}
	}
}

func TestBufferedRangeReaderChecksMemoryPerRange(t *testing.T) {
	setMinFreeMemory(t, 1)
	rr := NewBufferedRangeReader(bytes.NewReader([]byte("data")), 4)
	first, err := rr.RangeRead(context.Background(), http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := first.(utils.ReadCloser); !ok {
		t.Skip("not enough available memory to exercise the buffered path")
	}

	conf.MinFreeMemory = 0
	second, err := rr.RangeRead(context.Background(), http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, ok := second.(*RateLimitReader); !ok {
		t.Fatalf("expected unbuffered fallback for the next range, got %T", second)
	}
}

func TestBufferedRangeReaderEmptyRangeIsUnbuffered(t *testing.T) {
	setMinFreeMemory(t, 1)
	data := testData(100)
	f := &countingReaderAt{data: data}
	rr := NewBufferedRangeReader(f, int64(len(data)))

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Start: 100, Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if _, ok := rc.(*RateLimitReader); !ok {
		t.Fatalf("expected empty range to skip buffering, got %T", rc)
	}
	if ops := f.ops(); len(ops) != 0 {
		t.Fatalf("expected no server read, got %v", ops)
	}
}
