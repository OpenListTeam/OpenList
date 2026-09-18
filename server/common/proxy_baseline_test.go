package common

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type baselineRangeReader struct {
	data   []byte
	opens  atomic.Int64
	closes atomic.Int64
	err    error
}

func (r *baselineRangeReader) RangeRead(ctx context.Context, requested http_range.Range) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.err != nil {
		return nil, r.err
	}
	r.opens.Add(1)
	end := int64(len(r.data))
	if requested.Length >= 0 && requested.Start+requested.Length < end {
		end = requested.Start + requested.Length
	}
	return &baselineBody{
		Reader: bytes.NewReader(r.data[requested.Start:end]),
		closes: &r.closes,
	}, nil
}

func TestProxyBaselineReaderFailureAndCancellation(t *testing.T) {
	oldConf := conf.Conf
	conf.Conf = conf.DefaultConfig("data")
	t.Cleanup(func() { conf.Conf = oldConf })
	// Partitioned reader failures currently panic in net.ServeHTTP; see Stage 0 evidence.
	for _, mode := range []string{"range reader"} {
		for _, failed := range []bool{false, true} {
			name := "cancelled"
			if failed {
				name = "reader failure"
			}
			t.Run(mode+"/"+name, func(t *testing.T) {
				link, reader := baselineProxyLink(t, []byte("0123456789abcdef"), mode)
				ctx := context.Background()
				if failed {
					reader.err = errors.New("fixture read failure")
				} else {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				r := httptest.NewRequest(http.MethodGet, "/proxy/fixture.bin", nil).WithContext(ctx)
				w := httptest.NewRecorder()
				file := &model.Object{Name: "fixture.bin", Size: 16}
				if err := Proxy(w, r, link, file); err != nil {
					t.Fatal(err)
				}
				if w.Code != http.StatusRequestedRangeNotSatisfiable || reader.opens.Load() != reader.closes.Load() {
					t.Fatalf("status = %d, bodies opened/closed = %d/%d", w.Code, reader.opens.Load(), reader.closes.Load())
				}
			})
		}
	}
}

type baselineBody struct {
	*bytes.Reader
	closes *atomic.Int64
}

func (b *baselineBody) Close() error {
	b.closes.Add(1)
	return nil
}

func baselineProxyLink(t testing.TB, data []byte, mode string) (*model.Link, *baselineRangeReader) {
	t.Helper()
	link := &model.Link{}
	var reader *baselineRangeReader
	switch mode {
	case "transparent URL", "partitioned URL":
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.ServeContent(w, r, "fixture.bin", time.Unix(1_700_000_000, 0), bytes.NewReader(data))
		}))
		t.Cleanup(upstream.Close)
		link.URL = upstream.URL
	case "range reader", "partitioned range reader":
		reader = &baselineRangeReader{data: data}
		link.RangeReader = reader
	default:
		t.Fatalf("unknown proxy mode %q", mode)
	}
	if mode == "partitioned URL" || mode == "partitioned range reader" {
		link.Concurrency = 2
		link.PartSize = 4
	}
	return link, reader
}

func TestProxyBaselineModes(t *testing.T) {
	oldConf := conf.Conf
	conf.Conf = conf.DefaultConfig("data")
	t.Cleanup(func() { conf.Conf = oldConf })

	data := []byte("0123456789abcdef")
	file := &model.Object{Name: "fixture.bin", Size: int64(len(data)), Modified: time.Unix(1_700_000_000, 0)}
	for _, mode := range []string{"transparent URL", "partitioned URL", "range reader", "partitioned range reader"} {
		for _, request := range []struct {
			name, method, rangeHeader, body, contentRange string
			status, length                                int
		}{
			{name: "full GET", method: http.MethodGet, body: string(data), status: http.StatusOK, length: len(data)},
			{name: "HEAD", method: http.MethodHead, status: http.StatusOK, length: len(data)},
			{name: "Range GET", method: http.MethodGet, rangeHeader: "bytes=2-5", body: "2345", contentRange: "bytes 2-5/16", status: http.StatusPartialContent, length: 4},
		} {
			t.Run(mode+"/"+request.name, func(t *testing.T) {
				link, reader := baselineProxyLink(t, data, mode)
				r := httptest.NewRequest(request.method, "/proxy/fixture.bin", nil)
				if request.rangeHeader != "" {
					r.Header.Set("Range", request.rangeHeader)
				}
				w := httptest.NewRecorder()
				if err := Proxy(w, r, link, file); err != nil {
					t.Fatal(err)
				}
				if w.Code != request.status || w.Body.String() != request.body {
					t.Fatalf("status/body = %d/%q, want %d/%q", w.Code, w.Body.String(), request.status, request.body)
				}
				if got := w.Header().Get("Content-Length"); got != strconv.Itoa(request.length) {
					t.Errorf("Content-Length = %q, want %d", got, request.length)
				}
				if got := w.Header().Get("Content-Range"); got != request.contentRange {
					t.Errorf("Content-Range = %q, want %q", got, request.contentRange)
				}
				if got := w.Header().Get("Content-Disposition"); got != utils.GenerateContentDisposition(file.Name) {
					t.Errorf("Content-Disposition = %q", got)
				}
				if reader != nil && reader.opens.Load() != reader.closes.Load() {
					t.Errorf("range bodies opened/closed = %d/%d", reader.opens.Load(), reader.closes.Load())
				}
			})
		}
	}
}

func TestProxyBaselineURLHeaders(t *testing.T) {
	oldConf := conf.Conf
	conf.Conf = conf.DefaultConfig("data")
	t.Cleanup(func() { conf.Conf = oldConf })
	oldIgnored, hadIgnored := conf.SlicesMap[conf.ProxyIgnoreHeaders]
	conf.SlicesMap[conf.ProxyIgnoreHeaders] = []string{"x-ignored"}
	t.Cleanup(func() {
		if hadIgnored {
			conf.SlicesMap[conf.ProxyIgnoreHeaders] = oldIgnored
		} else {
			delete(conf.SlicesMap, conf.ProxyIgnoreHeaders)
		}
	})

	for _, partitioned := range []bool{false, true} {
		name := "transparent"
		if partitioned {
			name = "partitioned"
		}
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Allowed") != "forwarded" || r.Header.Get("X-Override") != "link" || r.Header.Get("X-Ignored") != "" {
					t.Errorf("upstream headers: allowed=%q override=%q ignored=%q", r.Header.Get("X-Allowed"), r.Header.Get("X-Override"), r.Header.Get("X-Ignored"))
				}
				http.ServeContent(w, r, "fixture.bin", time.Unix(1_700_000_000, 0), bytes.NewReader([]byte("0123456789abcdef")))
			}))
			t.Cleanup(upstream.Close)
			link := &model.Link{URL: upstream.URL, Header: http.Header{"X-Override": []string{"link"}}}
			if partitioned {
				link.Concurrency, link.PartSize = 2, 4
			}
			r := httptest.NewRequest(http.MethodGet, "/proxy/fixture.bin", nil)
			r.Header.Set("X-Allowed", "forwarded")
			r.Header.Set("X-Override", "client")
			r.Header.Set("X-Ignored", "drop")
			w := httptest.NewRecorder()
			if err := Proxy(w, r, link, &model.Object{Name: "fixture.bin", Size: 16}); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || w.Body.String() != "0123456789abcdef" {
				t.Fatalf("status/body = %d/%q", w.Code, w.Body.String())
			}
		})
	}
}

func TestProxyBaselineTransparentURLFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failure", http.StatusServiceUnavailable)
	}))
	t.Cleanup(upstream.Close)
	link := &model.Link{URL: upstream.URL}
	file := &model.Object{Name: "fixture.bin", Size: 16}
	for _, cancelled := range []bool{false, true} {
		name := "upstream 503"
		if cancelled {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/proxy/fixture.bin", nil)
			if cancelled {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			err := Proxy(w, r, link, file)
			if err == nil {
				t.Fatal("Proxy error = nil, want upstream/cancellation error")
			}
			if w.Body.Len() != 0 {
				t.Errorf("response body = %q, want empty before caller maps error", w.Body.String())
			}
			if cancelled && !errors.Is(err, context.Canceled) {
				t.Errorf("Proxy error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestProxyBaselinePartitionedURLFailure(t *testing.T) {
	oldConf := conf.Conf
	conf.Conf = conf.DefaultConfig("data")
	t.Cleanup(func() { conf.Conf = oldConf })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failure", http.StatusServiceUnavailable)
	}))
	t.Cleanup(upstream.Close)
	link := &model.Link{URL: upstream.URL, Concurrency: 2, PartSize: 4}
	w := httptest.NewRecorder()
	err := Proxy(w, httptest.NewRequest(http.MethodGet, "/proxy/fixture.bin", nil), link, &model.Object{Name: "fixture.bin", Size: 16})
	// Current behavior: streaming has already written 200 when the retry failure
	// reaches ServeHTTP. This is characterization, not an endorsed error mapping.
	if !errors.Is(err, context.Canceled) || w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("Proxy error/status/body = %v/%d/%q, want context.Canceled/200/empty", err, w.Code, w.Body.String())
	}
}

type baselineResponseWriter struct {
	header http.Header
	bytes  int
}

func (w *baselineResponseWriter) Header() http.Header { return w.header }
func (w *baselineResponseWriter) WriteHeader(int)     {}
func (w *baselineResponseWriter) Write(p []byte) (int, error) {
	w.bytes += len(p)
	return len(p), nil
}

func BenchmarkProxyBaseline(b *testing.B) {
	oldConf := conf.Conf
	conf.Conf = conf.DefaultConfig("data")
	b.Cleanup(func() { conf.Conf = oldConf })
	for _, size := range []int{64 << 10, 1 << 20} {
		data := bytes.Repeat([]byte("a"), size)
		file := &model.Object{Name: "fixture.bin", Size: int64(size), Modified: time.Unix(1_700_000_000, 0)}
		for _, mode := range []string{"transparent URL", "partitioned URL", "range reader", "partitioned range reader"} {
			b.Run(mode+"/"+strconv.Itoa(size), func(b *testing.B) {
				link, _ := baselineProxyLink(b, data, mode)
				if link.PartSize > 0 {
					link.PartSize = 32 << 10
				}
				b.SetBytes(int64(size))
				b.ReportAllocs()
				var failures atomic.Int64
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						w := &baselineResponseWriter{header: make(http.Header)}
						r := httptest.NewRequest(http.MethodGet, "/proxy/fixture.bin", nil)
						if err := Proxy(w, r, link, file); err != nil || w.bytes != size {
							failures.Add(1)
						}
					}
				})
				if n := failures.Load(); n != 0 {
					b.Fatalf("%d proxy operations failed or returned incomplete bytes", n)
				}
			})
		}
	}
}
