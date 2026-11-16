package stream

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/mem"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"golang.org/x/time/rate"
)

type countingReaderAt struct {
	*bytes.Reader
	calls int
	bytes int
}

func (r *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.calls++
	n, err := r.Reader.ReadAt(p, off)
	r.bytes += n
	return n, err
}

func TestRangeReader(t *testing.T) {
	if err := mem.MemoryGrowCheck(windowSize); err != nil {
		t.Skip(err)
	}

	t.Run("PrefetchesWindows", func(t *testing.T) {
		data := bytes.Repeat([]byte("range data"), windowSize)
		src := &countingReaderAt{Reader: bytes.NewReader(data)}
		rc, err := NewBufferedRangeReader(src, int64(len(data))).RangeRead(context.Background(), http_range.Range{Length: -1})
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()

		read := func(r io.Reader) {
			t.Helper()
			var out bytes.Buffer
			if _, err := io.CopyBuffer(struct{ io.Writer }{&out}, r, make([]byte, 4<<10)); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), data) {
				t.Fatal("content mismatch")
			}
		}
		read(rc)
		bufferedCalls := src.calls
		src.calls = 0
		read(io.NewSectionReader(src, 0, int64(len(data))))
		if bufferedCalls != len(data)/windowSize || src.calls != len(data)/(4<<10) {
			t.Fatalf("upstream calls: buffered=%d, unbuffered=%d", bufferedCalls, src.calls)
		}
		t.Logf("upstream calls: %d -> %d (256x fewer)", src.calls, bufferedCalls)
	})

	t.Run("LimitsAbandonedPrefetch", func(t *testing.T) {
		const requests = 4
		const speed = 8 * windowSize // bytes per second; allow one window of burst.
		previous := ServerDownloadLimit
		t.Cleanup(func() { ServerDownloadLimit = previous })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		data := bytes.Repeat([]byte{42}, 8*windowSize)
		src := &countingReaderAt{Reader: bytes.NewReader(data)}
		ranges := NewBufferedRangeReader(src, int64(len(data)))
		start := time.Now()
		ServerDownloadLimit = rate.NewLimiter(rate.Limit(speed), windowSize)
		for range requests {
			rc, err := ranges.RangeRead(ctx, http_range.Range{Length: -1})
			if err != nil {
				t.Fatal(err)
			}
			var p [16]byte
			_, readErr := io.ReadFull(rc, p[:])
			closeErr := rc.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
		}
		elapsed := time.Since(start)
		if src.bytes != requests*windowSize {
			t.Fatalf("upstream bytes = %d, want %d", src.bytes, requests*windowSize)
		}
		minimum := time.Duration(src.bytes-windowSize) * time.Second / speed
		if elapsed < minimum {
			t.Fatalf("fetched %d bytes in %s, want at least %s at %d bytes/s with %d burst",
				src.bytes, elapsed, minimum, speed, windowSize)
		}
	})

	t.Run("RandomReadReleasesWindows", func(t *testing.T) {
		const size, off = 6 * windowSize, 5 * windowSize
		data := bytes.Repeat([]byte{42}, size)
		buffered := NewBufferedRangeReader(bytes.NewReader(data), size)
		var readers []*bufferedRangeReader
		rr := RangeReaderFunc(func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
			rc, err := buffered.RangeRead(ctx, r)
			if err != nil {
				return nil, err
			}
			br, ok := rc.(*RateLimitReader).Reader.(*bufferedRangeReader)
			if !ok {
				t.Fatal("expected buffered reader")
			}
			readers = append(readers, br)
			return rc, nil
		})
		ss, err := NewSeekableStream(&FileStream{
			Obj: &model.Object{Size: size},
			Ctx: context.Background(),
		}, &model.Link{RangeReader: rr, ContentLength: size})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ss.Close() })
		ra, err := NewReadAtSeeker(ss, 0)
		if err != nil {
			t.Fatal(err)
		}
		for range 20 {
			var p [1]byte
			if _, err := ra.ReadAt(p[:], off); err != nil {
				t.Fatal(err)
			}
			if p[0] != data[off] {
				t.Fatal("content mismatch")
			}
			for _, r := range readers[:len(readers)-1] {
				if r.buffer != nil {
					t.Fatal("replaced reader still retains its window")
				}
			}
		}
		if err := ss.Close(); err != nil {
			t.Fatal(err)
		}
		for _, r := range readers {
			if r.buffer != nil {
				t.Fatal("closed reader still retains its window")
			}
		}
	})
}
