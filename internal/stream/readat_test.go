package stream_test

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"sync/atomic"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

// maxReuseGap mirrors the internal continuation-reuse window (4*utils.MB).
const maxReuseGap = 4 * 1024 * 1024

// newMockSeekableStream builds a SeekableStream whose range reads are served
// from data, counting every upstream range request in gets.
func newMockSeekableStream(t *testing.T, data []byte, gets *atomic.Int64) *stream.SeekableStream {
	t.Helper()
	rr := stream.RangeReaderFunc(func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
		gets.Add(1)
		if r.Length < 0 || r.Start+r.Length > int64(len(data)) {
			r.Length = int64(len(data)) - r.Start
		}
		return io.NopCloser(io.NewSectionReader(bytes.NewReader(data), r.Start, r.Length)), nil
	})
	ss, err := stream.NewSeekableStream(&stream.FileStream{
		Obj: &model.Object{Size: int64(len(data))},
		Ctx: context.Background(),
	}, &model.Link{
		RangeReader:   rr,
		ContentLength: int64(len(data)),
	})
	if err != nil {
		t.Fatalf("NewSeekableStream() error = %v", err)
	}
	return ss
}

// readAtFull reads len(p) bytes at off and fails the test on mismatch.
func readAtFull(t *testing.T, ra io.ReaderAt, data []byte, off int64, p []byte) {
	t.Helper()
	n, err := ra.ReadAt(p, off)
	if err != nil {
		t.Fatalf("ReadAt(off=%d) error = %v", off, err)
	}
	if !bytes.Equal(p, data[off:off+int64(n)]) {
		t.Fatalf("ReadAt(off=%d) content mismatch", off)
	}
}

func randomData(size int) []byte {
	data := make([]byte, size)
	x := uint64(42)
	for i := range data {
		x = x*6364136223846793005 + 1
		data[i] = byte(x >> 33)
	}
	return data
}

// Sequential reads must reuse a single upstream range request.
func TestReadAtSeekerSequentialReuse(t *testing.T) {
	data := randomData(16 * 1024 * 1024)
	var gets atomic.Int64
	ss := newMockSeekableStream(t, data, &gets)
	ra, err := stream.NewReadAtSeeker(ss, 0, true)
	if err != nil {
		t.Fatalf("NewReadAtSeeker() error = %v", err)
	}
	buf := make([]byte, 128*1024)
	for off := 0; off < len(data); off += len(buf) {
		readAtFull(t, ra, data, int64(off), buf)
	}
	if n := gets.Load(); n != 1 {
		t.Fatalf("sequential read issued %d range requests, want 1", n)
	}
}

// A read landing up to maxReuseGap bytes past a parked reader must be served
// by advancing that reader, without a new range request.
func TestReadAtSeekerSkipsAheadWithinWindow(t *testing.T) {
	data := randomData(16 * 1024 * 1024)
	var gets atomic.Int64
	ss := newMockSeekableStream(t, data, &gets)
	ra, err := stream.NewReadAtSeeker(ss, 0, true)
	if err != nil {
		t.Fatalf("NewReadAtSeeker() error = %v", err)
	}
	// Park a continuation reader right after reading the first 2 MiB.
	chunk := make([]byte, 256*1024)
	for off := 0; off < 2*1024*1024; off += len(chunk) {
		readAtFull(t, ra, data, int64(off), chunk)
	}
	skip := 512 * 1024
	off := int64(2*1024*1024 + skip)
	readAtFull(t, ra, data, off, chunk)
	if n := gets.Load(); n != 1 {
		t.Fatalf("window skip issued %d range requests, want 1", n)
	}
	// A second skip deeper inside the window must also be free.
	off = int64(4*1024*1024) - 128*1024
	readAtFull(t, ra, data, off, chunk)
	if n := gets.Load(); n != 1 {
		t.Fatalf("second window skip issued %d range requests, want 1", n)
	}
}

// A forward jump beyond the reuse window must open a new range request but
// keep the parked reader available for later window hits.
func TestReadAtSeekerFarJumpOpensNewRequest(t *testing.T) {
	data := randomData(16 * 1024 * 1024)
	var gets atomic.Int64
	ss := newMockSeekableStream(t, data, &gets)
	ra, err := stream.NewReadAtSeeker(ss, 0, true)
	if err != nil {
		t.Fatalf("NewReadAtSeeker() error = %v", err)
	}
	buf := make([]byte, 256*1024)
	for off := 0; off < 2*1024*1024; off += len(buf) {
		readAtFull(t, ra, data, int64(off), buf)
	}
	// 2 MiB -> 10 MiB is beyond the 4 MiB reuse window.
	off := int64(10 * 1024 * 1024)
	readAtFull(t, ra, data, off, buf)
	if n := gets.Load(); n != 2 {
		t.Fatalf("far jump issued %d range requests, want 2", n)
	}
	// Back within the window of the 10 MiB chain: free reuse again.
	readAtFull(t, ra, data, off+maxReuseGap, buf)
	if n := gets.Load(); n != 2 {
		t.Fatalf("jump inside new window issued %d range requests, want 2", n)
	}
}

// Backward reads can never reuse a parked continuation and must open a new
// range request.
func TestReadAtSeekerBackwardJumpOpensNewRequest(t *testing.T) {
	data := randomData(8 * 1024 * 1024)
	var gets atomic.Int64
	ss := newMockSeekableStream(t, data, &gets)
	ra, err := stream.NewReadAtSeeker(ss, 0, true)
	if err != nil {
		t.Fatalf("NewReadAtSeeker() error = %v", err)
	}
	buf := make([]byte, 256*1024)
	for off := 0; off < 2*1024*1024; off += len(buf) {
		readAtFull(t, ra, data, int64(off), buf)
	}
	readAtFull(t, ra, data, int64(1024*1024), buf)
	if n := gets.Load(); n != 2 {
		t.Fatalf("backward jump issued %d range requests, want 2", n)
	}
}

// Random reads must return correct data and keep upstream requests bounded:
// each read is either a window hit or a fresh request, never more than one.
func TestReadAtSeekerRandomReads(t *testing.T) {
	data := randomData(32 * 1024 * 1024)
	var gets atomic.Int64
	ss := newMockSeekableStream(t, data, &gets)
	ra, err := stream.NewReadAtSeeker(ss, 0, true)
	if err != nil {
		t.Fatalf("NewReadAtSeeker() error = %v", err)
	}
	const chunk = 8 * 1024
	buf := make([]byte, chunk)
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 200; i++ {
		off := r.Int63n(int64(len(data)) - chunk)
		readAtFull(t, ra, data, off, buf)
	}
	if n := gets.Load(); n > 200 {
		t.Fatalf("random reads issued %d range requests, want <= 200", n)
	}
}
