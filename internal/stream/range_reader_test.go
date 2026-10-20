package stream

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
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

func TestRangeReaderReadsWholeWindows(t *testing.T) {
	data := testData(3*windowSize + 1234)
	f := &countingReaderAt{data: data}
	rr := newRangeReader(f, int64(len(data)), &windowPool)

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
	rr := newRangeReader(f, int64(len(data)), &windowPool)

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
	rr := newRangeReader(f, int64(len(data)), &windowPool)

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
	rr := newRangeReader(f, int64(len(data)), &windowPool)

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
	rr := newRangeReader(f, -1, &windowPool)

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
	rr := newRangeReader(f, int64(len(data)), &windowPool)

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
	rr := NewBufferedRangeReader(f, int64(len(data)))

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
	rr := newRangeReader(f, int64(len(data)), &windowPool)

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

func TestRangeReaderDegradedWindow(t *testing.T) {
	data := testData(windowSize + 1234)
	f := &countingReaderAt{data: data}
	rr := newRangeReader(f, int64(len(data)), &degradedPool)

	rc, err := rr.RangeRead(context.Background(), http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, rc, 64*1024); !bytes.Equal(got, data) {
		t.Fatal("content mismatch")
	}
	ops := f.ops()
	if len(ops) != int((int64(len(data))+degradedWindowSize-1)/degradedWindowSize) {
		t.Fatalf("expected degraded windows, got %v", ops)
	}
	if ops[0].n != degradedWindowSize {
		t.Fatalf("expected first read of %d bytes, got %d", degradedWindowSize, ops[0].n)
	}
}

func TestRangeReaderCloseOnce(t *testing.T) {
	data := testData(100)
	buffer := bufio.NewReaderSize(nil, degradedWindowSize)
	// Reuse the same buffer even if GC clears the pool between ranges.
	pool := &sync.Pool{New: func() any { return buffer }}
	rr := newRangeReader(bytes.NewReader(data), int64(len(data)), pool)
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
	rr := newRangeReader(f, int64(len(data)), &windowPool)
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
