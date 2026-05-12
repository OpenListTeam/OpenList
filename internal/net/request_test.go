package net

//no http range
//

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

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

func TestDownloadOrder(t *testing.T) {
	buff := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	downloader, invocations, ranges := newDownloadRangeClient(buff)
	con, partSize := 3, 3
	d := NewDownloader(func(d *Downloader) {
		d.Concurrency = con
		d.PartSize = partSize
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
