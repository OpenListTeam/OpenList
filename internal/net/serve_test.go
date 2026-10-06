package net

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

func TestServeHTTPClosesMultipartRangeBeforeOpeningNext(t *testing.T) {
	source := newSequentialRangeSource("abc")
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/file", nil).WithContext(ctx)
	request.Header.Set("Range", "bytes=0-0,2-2")
	recorder := httptest.NewRecorder()

	if err := ServeHTTP(recorder, request, "file.txt", time.Time{}, 3, source); err != nil {
		t.Fatalf("ServeHTTP() error = %v", err)
	}
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusPartialContent)
	}

	mediaType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse Content-Type: %v", err)
	}
	if mediaType != "multipart/byteranges" {
		t.Fatalf("Content-Type = %q, want multipart/byteranges", mediaType)
	}
	multipartReader := multipart.NewReader(response.Body, params["boundary"])
	var parts []string
	for {
		part, err := multipartReader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read multipart part: %v", err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read multipart body: %v", err)
		}
		parts = append(parts, string(body))
	}
	if want := []string{"a", "c"}; !reflect.DeepEqual(parts, want) {
		t.Fatalf("multipart parts = %q, want %q", parts, want)
	}
	assertRangeLifecycle(t, source, []string{"open:0", "close:0", "open:2", "close:2"}, []int{1, 1})
}

func TestServeHTTPClosesSelectedRangeBody(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		rangeValue string
		wantStatus int
		wantEvents []string
	}{
		{name: "full", method: http.MethodGet, wantStatus: http.StatusOK, wantEvents: []string{"open:0", "close:0"}},
		{name: "single range", method: http.MethodGet, rangeValue: "bytes=1-1", wantStatus: http.StatusPartialContent, wantEvents: []string{"open:1", "close:1"}},
		{name: "head", method: http.MethodHead, wantStatus: http.StatusOK, wantEvents: []string{"open:0", "close:0"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := newSequentialRangeSource("abc")
			request := httptest.NewRequest(test.method, "/file", nil)
			if test.rangeValue != "" {
				request.Header.Set("Range", test.rangeValue)
			}
			recorder := httptest.NewRecorder()

			if err := ServeHTTP(recorder, request, "file.txt", time.Time{}, 3, source); err != nil {
				t.Fatalf("ServeHTTP() error = %v", err)
			}
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			assertRangeLifecycle(t, source, test.wantEvents, []int{1})
		})
	}
}

func TestServeHTTPClosesRangeAfterWriteFailure(t *testing.T) {
	writeErr := errors.New("write failed")
	source := newSequentialRangeSource("abc")
	request := httptest.NewRequest(http.MethodGet, "/file", nil)
	writer := &failingResponseWriter{header: make(http.Header), err: writeErr}

	err := ServeHTTP(writer, request, "file.txt", time.Time{}, 3, source)
	if !errors.Is(err, writeErr) {
		t.Fatalf("ServeHTTP() error = %v, want %v", err, writeErr)
	}
	assertRangeLifecycle(t, source, []string{"open:0", "close:0"}, []int{1})
}

func TestServeHTTPClosesBodyReturnedWithOpenError(t *testing.T) {
	source := newSequentialRangeSource("abc")
	source.openErr = HttpStatusCodeError(http.StatusServiceUnavailable)
	source.closeErr = errors.New("close failed")
	request := httptest.NewRequest(http.MethodGet, "/file", nil)
	recorder := httptest.NewRecorder()

	if err := ServeHTTP(recorder, request, "file.txt", time.Time{}, 3, source); err != nil {
		t.Fatalf("ServeHTTP() error = %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	assertRangeLifecycle(t, source, []string{"open:0", "close:0"}, []int{1})
}

func TestServeHTTPStopsMultipartAfterRangeCloseFailure(t *testing.T) {
	closeErr := errors.New("close failed")
	source := newSequentialRangeSource("abc")
	source.closeErr = closeErr
	request := httptest.NewRequest(http.MethodGet, "/file", nil)
	request.Header.Set("Range", "bytes=0-0,2-2")
	recorder := httptest.NewRecorder()

	err := ServeHTTP(recorder, request, "file.txt", time.Time{}, 3, source)
	if !errors.Is(err, closeErr) {
		t.Fatalf("ServeHTTP() error = %v, want %v", err, closeErr)
	}
	assertRangeLifecycle(t, source, []string{"open:0", "close:0"}, []int{1})
}

type sequentialRangeSource struct {
	content []byte
	permit  chan struct{}

	mu          sync.Mutex
	events      []string
	closeCounts []int
	closeErr    error
	openErr     error
}

func newSequentialRangeSource(content string) *sequentialRangeSource {
	return &sequentialRangeSource{
		content: []byte(content),
		permit:  make(chan struct{}, 1),
	}
}

func (s *sequentialRangeSource) RangeRead(ctx context.Context, requested http_range.Range) (io.ReadCloser, error) {
	select {
	case s.permit <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	start := int(requested.Start)
	length := int(requested.Length)
	if length < 0 || start+length > len(s.content) {
		length = len(s.content) - start
	}
	end := start + length
	s.mu.Lock()
	index := len(s.closeCounts)
	s.events = append(s.events, fmt.Sprintf("open:%d", requested.Start))
	s.closeCounts = append(s.closeCounts, 0)
	s.mu.Unlock()
	return &testReadCloser{
		Reader: bytes.NewReader(s.content[start:end]),
		close: func() error {
			s.mu.Lock()
			s.closeCounts[index]++
			closeCalls := s.closeCounts[index]
			if closeCalls == 1 {
				s.events = append(s.events, fmt.Sprintf("close:%d", requested.Start))
			}
			s.mu.Unlock()
			if closeCalls != 1 {
				return fmt.Errorf("body closed %d times", closeCalls)
			}
			<-s.permit
			return s.closeErr
		},
	}, s.openErr
}

func (s *sequentialRangeSource) eventsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func (s *sequentialRangeSource) closeCountsSnapshot() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.closeCounts...)
}

func assertRangeLifecycle(t *testing.T, source *sequentialRangeSource, wantEvents []string, wantCloseCounts []int) {
	t.Helper()
	if got := source.eventsSnapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Fatalf("range lifecycle = %v, want %v", got, wantEvents)
	}
	if got := source.closeCountsSnapshot(); !reflect.DeepEqual(got, wantCloseCounts) {
		t.Fatalf("close counts = %v, want %v", got, wantCloseCounts)
	}
}

type failingResponseWriter struct {
	header http.Header
	err    error
}

func (w *failingResponseWriter) Header() http.Header { return w.header }
func (*failingResponseWriter) WriteHeader(int)       {}
func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type testReadCloser struct {
	io.Reader
	close func() error
}

func (b *testReadCloser) Close() error { return b.close() }
