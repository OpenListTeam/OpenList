package net

//no http range
//

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	hcache "github.com/OpenListTeam/OpenList/v4/internal/hybrid_cache"
	"github.com/OpenListTeam/OpenList/v4/internal/mem"
	"github.com/OpenListTeam/OpenList/v4/pkg/buffer"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/sirupsen/logrus"
)

func containsString(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}

func withDownloaderConfig(t *testing.T) {
	t.Helper()
	previousConf := conf.Conf
	previousPolicy := conf.CachePolicy
	conf.Conf = &conf.Config{TempDir: t.TempDir()}
	conf.CachePolicy = cache.PolicyMemory
	t.Cleanup(func() {
		conf.Conf = previousConf
		conf.CachePolicy = previousPolicy
	})
}

func TestDownloaderMemoryCeiling(t *testing.T) {
	tests := []struct {
		name        string
		rangeLength int64
		concurrency int
		partSize    int
		want        int64
	}{
		{"working set", 100 << 20, 2, 8 << 20, 16 << 20},
		{"partial final block", 6, 2, 4, 8},
		{"range smaller than pool", 10, 4, 8, 16},
		{"exact block", 8, 2, 4, 8},
		{"multiplication overflow", 100, int(^uint(0) >> 1), 2, 100},
		{"negative range", -1, 2, 4, 0},
		{"invalid concurrency", 8, 0, 4, 0},
		{"invalid part size", 8, 2, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := downloaderMemoryCeiling(tt.rangeLength, tt.concurrency, tt.partSize); got != tt.want {
				t.Fatalf("downloaderMemoryCeiling() = %d, want %d", got, tt.want)
			}
		})
	}
	if strconv.IntSize == 64 {
		maxInt := int(^uint(0) >> 1)
		if got := downloaderMemoryCeiling(math.MaxInt64, maxInt, 2); got != math.MaxInt64 {
			t.Fatalf("downloaderMemoryCeiling() overflow result = %d, want MaxInt64", got)
		}
	}
}

func TestTryDownloadChunkReturnsPrefetchAllocationError(t *testing.T) {
	withDownloaderConfig(t)
	hc, err := hcache.NewHybridCache(4, 4, cache.PolicyMemory)
	if err != nil {
		t.Fatalf("NewHybridCache() error = %v", err)
	}
	t.Cleanup(func() { _ = hc.Close() })
	block, err := hc.NextBlock()
	if err != nil {
		t.Fatalf("NextBlock() error = %v", err)
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	d := &downloader{
		ctx:    ctx,
		cancel: cancel,
		cfg: Downloader{
			PartSize:    4,
			Concurrency: 2,
			HttpClient: func(context.Context, *HttpRequestParams) (*http.Response, error) {
				return &http.Response{
					Body:          io.NopCloser(bytes.NewReader([]byte("1234"))),
					Header:        http.Header{"Content-Range": {"bytes 0-3/8"}},
					ContentLength: 4,
				}, nil
			},
		},
		params: &HttpRequestParams{
			Range: http_range.Range{Length: 8},
			Size:  8,
		},
		chunkCh:     make(chan chunk, 2),
		bufMap:      make(map[int]*buffer.PipeBuffer),
		concurrency: 1,
		pos:         4,
		maxPos:      8,
		nextChunk:   1,
		hc:          hc,
	}
	current := &chunk{start: 0, size: 4, id: 0, buf: buffer.NewPipeBuffer(ctx, block)}
	if _, err := d.tryDownloadChunk(d.getParamsFromChunk(current), current); !errors.Is(err, mem.ErrNotEnoughMemory) {
		t.Fatalf("tryDownloadChunk() error = %v, want ErrNotEnoughMemory", err)
	}
}

func TestDownloadOrder(t *testing.T) {
	withDownloaderConfig(t)
	buff := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	downloader, invocations, ranges := newDownloadRangeClient(buff)
	con, partSize := 3, 3
	d := NewDownloader(func(d *Downloader) {
		d.Concurrency = con
		d.PartSize = partSize
		d.CachePolicy = cache.PolicyMemory
		d.HttpClient = downloader.HttpRequest
	})

	var start, length int64 = 2, 10
	length2 := length
	if length2 == -1 {
		length2 = int64(len(buff)) - start
	}
	req := &HttpRequestParams{
		Range: http_range.Range{Start: start, Length: length},
		Size:  int64(len(buff)),
	}
	readCloser, err := d.Download(context.Background(), req)

	if err != nil {
		t.Fatalf("expect no error, got %v", err)
	}
	resultBuf, err := io.ReadAll(readCloser)
	if err != nil {
		t.Fatalf("expect no error, got %v", err)
	}
	if exp, a := buff[start:start+length2], resultBuf; !bytes.Equal(exp, a) {
		t.Errorf("expect buffer %v, got %v", exp, a)
	}
	chunkSize := int(length+int64(partSize)-1) / partSize
	if e, a := chunkSize, *invocations; e != a {
		t.Errorf("expect %v API calls, got %v", e, a)
	}

	expectRngs := []string{"2-1", "6-3", "3-3", "9-3"}
	for _, rng := range expectRngs {
		if !containsString(*ranges, rng) {
			t.Errorf("expect range %v, but absent in return", rng)
		}
	}
	if e, a := expectRngs, *ranges; len(e) != len(a) {
		t.Errorf("expect %v ranges, got %v", e, a)
	}
	if err := readCloser.Close(); err != nil {
		t.Errorf("expect no error on close, got %v", err)
	}
}

func TestDownloadInterrupt(t *testing.T) {
	withDownloaderConfig(t)
	buff := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	buff = append(buff, buff...)
	downloader, _, _ := newDownloadRangeClient(buff)
	con, partSize := 6, 3
	d := NewDownloader(func(d *Downloader) {
		d.Concurrency = con
		d.PartSize = partSize
		d.HttpClient = downloader.HttpRequest
		d.ConcurrencyLimit = &ConcurrencyLimit{
			Limit: 5,
		}
	})

	var start, length int64 = 0, int64(len(buff))
	req := &HttpRequestParams{
		Range: http_range.Range{Start: start, Length: length},
		Size:  int64(len(buff)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	readCloser, err := d.Download(ctx, req)

	if err != nil {
		t.Fatalf("expect no error, got %v", err)
	}
	_, err = io.CopyN(io.Discard, readCloser, 8)
	if err != nil {
		t.Fatalf("expect no error, got %v", err)
	}
	cancel()
	if err := readCloser.Close(); err != nil {
		t.Errorf("expect no error on close, got %v", err)
	}
}

func TestHighConcurrency(t *testing.T) {
	withDownloaderConfig(t)
	buff := make([]byte, 8<<10)
	for i := range len(buff) {
		buff[i] = byte(i % 256)
	}
	downloader, invocations, _ := newDownloadRangeClient(buff)
	con, partSize := 64, 100
	concurrencyLimit := uint32(32)
	d := NewDownloader(func(d *Downloader) {
		d.Concurrency = con
		d.PartSize = partSize
		d.CachePolicy = cache.PolicyMemory
		d.HttpClient = downloader.HttpRequest
		d.ConcurrencyLimit = &ConcurrencyLimit{
			Limit: concurrencyLimit,
		}
	})

	var start, length int64 = 2, 7 << 10
	length2 := length
	if length2 == -1 {
		length2 = int64(len(buff)) - start
	}
	req := &HttpRequestParams{
		Range: http_range.Range{Start: start, Length: length},
		Size:  int64(len(buff)),
	}
	readCloser, err := d.Download(context.Background(), req)

	if err != nil {
		t.Fatalf("expect no error, got %v", err)
	}
	resultBuf, err := io.ReadAll(readCloser)
	if err != nil {
		t.Fatalf("expect no error, got %v", err)
	}
	if !bytes.Equal(buff[start:start+length2], resultBuf) {
		t.Error("expect buffer content matches, but got mismatch")
	}
	chunkSize := int(length+int64(partSize)-1) / partSize
	if e, a := chunkSize, *invocations; e != a {
		t.Errorf("expect %v API calls, got %v", e, a)
	}
	if err := readCloser.Close(); err != nil {
		t.Errorf("expect no error on close, got %v", err)
	}
	for range 100 {
		time.Sleep(10 * time.Millisecond)
		if d.ConcurrencyLimit.Limit == concurrencyLimit {
			return
		}
	}
	t.Errorf("expect concurrency limit to be %v, got %v", concurrencyLimit, d.ConcurrencyLimit.Limit)
}

func init() {
	Formatter := new(logrus.TextFormatter)
	Formatter.TimestampFormat = "2006-01-02T15:04:05.999999999"
	Formatter.FullTimestamp = true
	Formatter.ForceColors = true
	logrus.SetFormatter(Formatter)
	logrus.SetLevel(logrus.DebugLevel)
	logrus.Debugf("Download start")
}

func TestDownloadSingle(t *testing.T) {
	withDownloaderConfig(t)
	buff := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	downloader, invocations, ranges := newDownloadRangeClient(buff)
	con, partSize := 1, 4
	d := NewDownloader(func(d *Downloader) {
		d.Concurrency = con
		d.PartSize = partSize
		d.HttpClient = downloader.HttpRequest
	})

	var start, length int64 = 2, 10
	req := &HttpRequestParams{
		Range: http_range.Range{Start: start, Length: length},
		Size:  int64(len(buff)),
	}

	readCloser, err := d.Download(context.Background(), req)

	if err != nil {
		t.Fatalf("expect no error, got %v", err)
	}
	resultBuf, err := io.ReadAll(readCloser)
	if err != nil {
		t.Fatalf("expect no error, got %v", err)
	}
	if exp, a := int(length), len(resultBuf); exp != a {
		t.Errorf("expect  buffer length=%d, got %d", exp, a)
	}
	if e, a := int(length+int64(partSize)-1)/partSize, *invocations; e != a {
		t.Errorf("expect %v API calls, got %v", e, a)
	}

	expectRngs := []string{"2-2", "4-4", "8-4"}
	for _, rng := range expectRngs {
		if !containsString(*ranges, rng) {
			t.Errorf("expect range %v, but absent in return", rng)
		}
	}
	if e, a := expectRngs, *ranges; len(e) != len(a) {
		t.Errorf("expect %v ranges, got %v", e, a)
	}
	if err := readCloser.Close(); err != nil {
		t.Errorf("expect no error on close, got %v", err)
	}
}

type downloadCaptureClient struct {
	mockedHttpRequest    func(params *HttpRequestParams) (*http.Response, error)
	GetObjectInvocations int

	RetrievedRanges []string

	lock sync.Mutex
}

func (c *downloadCaptureClient) HttpRequest(ctx context.Context, params *HttpRequestParams) (*http.Response, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.GetObjectInvocations++

	if params.Range.Length != 0 {
		c.RetrievedRanges = append(c.RetrievedRanges, fmt.Sprintf("%d-%d", params.Range.Start, params.Range.Length))
	}

	return c.mockedHttpRequest(params)
}

func newDownloadRangeClient(data []byte) (*downloadCaptureClient, *int, *[]string) {
	capture := &downloadCaptureClient{}

	capture.mockedHttpRequest = func(params *HttpRequestParams) (*http.Response, error) {
		start, fin := params.Range.Start, params.Range.Start+params.Range.Length
		if params.Range.Length == -1 || fin >= int64(len(data)) {
			fin = int64(len(data))
		}
		bodyBytes := data[start:fin]

		header := &http.Header{}
		header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, fin-1, len(data)))
		return &http.Response{
			Body:          io.NopCloser(bytes.NewReader(bodyBytes)),
			Header:        *header,
			ContentLength: int64(len(bodyBytes)),
		}, nil
	}

	return capture, &capture.GetObjectInvocations, &capture.RetrievedRanges
}
