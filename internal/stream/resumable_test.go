package stream

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/stretchr/testify/require"
)

// truncatingReader 只吐出 left 个字节，之后一直返回 ErrUnexpectedEOF，模拟 CDN 掐连接
type truncatingReader struct {
	r    io.Reader
	left int
}

func (t *truncatingReader) Read(p []byte) (int, error) {
	if t.left <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	if len(p) > t.left {
		p = p[:t.left]
	}
	n, err := t.r.Read(p)
	t.left -= n
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (*truncatingReader) Close() error { return nil }

func sliceOf(data []byte, r http_range.Range) []byte {
	end := int64(len(data))
	if r.Length >= 0 && r.Start+r.Length < end {
		end = r.Start + r.Length
	}
	if r.Start >= end {
		return nil
	}
	return data[r.Start:end]
}

func TestResumableReaderResumesAfterTruncate(t *testing.T) {
	data := bytes.Repeat([]byte("openlist"), 10000)
	var requests []int64
	rr := RangeReaderFunc(func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
		requests = append(requests, r.Start)
		return io.NopCloser(bytes.NewReader(sliceOf(data, r))), nil
	})

	cut := len(data) / 3
	r := newResumableReader(context.Background(), rr, int64(len(data)), &truncatingReader{r: bytes.NewReader(data), left: cut})

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.Len(t, requests, 1)
	require.Equal(t, int64(cut), requests[0], "should resume right after the truncated offset")
	require.NoError(t, r.Close())
}

func TestResumableReaderGivesUpAfterMaxAttempts(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 100)
	attempts := 0
	rr := RangeReaderFunc(func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
		attempts++
		return &truncatingReader{r: bytes.NewReader(sliceOf(data, r))}, nil
	})
	r := newResumableReader(context.Background(), rr, int64(len(data)), &truncatingReader{r: bytes.NewReader(data)})

	_, err := io.ReadAll(r)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, maxResumeAttempts, attempts)
}

func TestResumableReaderKeepsProgressAcrossRetries(t *testing.T) {
	data := bytes.Repeat([]byte("openlist"), 100)
	rr := RangeReaderFunc(func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
		return &truncatingReader{r: bytes.NewReader(sliceOf(data, r)), left: 100}, nil
	})
	r := newResumableReader(context.Background(), rr, int64(len(data)), &truncatingReader{r: bytes.NewReader(data), left: 100})

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, data, got)
}

func TestResumableReaderDoesNotResumeWhenSizeUnknown(t *testing.T) {
	rr := RangeReaderFunc(func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
		t.Fatal("should not resume when size is unknown")
		return nil, nil
	})
	r := newResumableReader(context.Background(), rr, 0, &truncatingReader{r: bytes.NewReader([]byte("abc")), left: 1})

	_, err := io.ReadAll(r)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestResumableReaderStopsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rr := RangeReaderFunc(func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
		t.Fatal("should not resume when context is canceled")
		return nil, nil
	})
	r := newResumableReader(ctx, rr, 100, &truncatingReader{r: bytes.NewReader([]byte("abc")), left: 1})

	_, err := io.ReadAll(r)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}
