package common

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

func TestProxyCancelledPartitionedReaderDoesNotPanic(t *testing.T) {
	oldConf := conf.Conf
	conf.Conf = conf.DefaultConfig("data")
	t.Cleanup(func() { conf.Conf = oldConf })
	link := &model.Link{
		Concurrency: 2,
		PartSize:    4,
		RangeReader: stream.RangeReaderFunc(func(ctx context.Context, requested http_range.Range) (io.ReadCloser, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader([]byte("0123456789abcdef")[requested.Start : requested.Start+requested.Length])), nil
		}),
	}
	file := &model.Object{Name: "fixture.bin", Size: 16}
	for range 32 {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("Proxy panicked on cancelled partitioned read: %v", recovered)
				}
			}()
			r := httptest.NewRequest(http.MethodGet, "/proxy/fixture.bin", nil)
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			w := httptest.NewRecorder()
			_ = Proxy(w, r.WithContext(ctx), link, file)
			if bytes.Contains(w.Body.Bytes(), []byte("0123456789abcdef")) {
				t.Errorf("cancelled response contained file contents: %q", w.Body.String())
			}
		}()
	}
}
