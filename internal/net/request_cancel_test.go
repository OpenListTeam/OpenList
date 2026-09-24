package net

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

func TestDownloadCancelledAcquisitionReturnsErrorAndReleasesLimit(t *testing.T) {
	const attempts = 32
	limits := make([]*ConcurrencyLimit, 0, attempts)
	for range attempts {
		limit := &ConcurrencyLimit{Limit: 1}
		limits = append(limits, limit)
		d := NewDownloader(func(d *Downloader) {
			d.Concurrency = 2
			d.PartSize = 4
			d.ConcurrencyLimit = limit
			d.OpenPart = func(ctx context.Context, _ PartRequest) (io.ReadCloser, error) {
				return nil, ctx.Err()
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		reader, err := d.Download(ctx, 16, http_range.Range{Length: -1})
		if reader == nil && err == nil {
			t.Error("cancelled download returned a nil reader and nil error")
		}
		if reader != nil {
			_ = reader.Close()
		} else if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled download error = %v, want context.Canceled", err)
		}
	}
	time.Sleep(50 * time.Millisecond) // allow any started workers to release their slots
	for i, limit := range limits {
		limit.mu.Lock()
		got := limit.Limit
		limit.mu.Unlock()
		if got != 1 {
			t.Errorf("attempt %d remaining concurrency = %d, want 1", i, got)
		}
	}
}

func TestDownloadSinglePartFailureReleasesLimit(t *testing.T) {
	upstreamErr := errors.New("upstream failure")
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "cancelled", ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}(), want: context.Canceled},
		{name: "upstream failure", ctx: context.Background(), want: upstreamErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := &ConcurrencyLimit{Limit: 1}
			d := NewDownloader(func(d *Downloader) {
				d.PartSize = 32
				d.ConcurrencyLimit = limit
				d.OpenPart = func(ctx context.Context, _ PartRequest) (io.ReadCloser, error) {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					return nil, upstreamErr
				}
			})
			reader, err := d.Download(tc.ctx, 16, http_range.Range{Length: -1})
			if reader != nil || !errors.Is(err, tc.want) {
				t.Fatalf("single-part failed download = %v, %v; want nil, %v", reader, err, tc.want)
			}
			limit.mu.Lock()
			got := limit.Limit
			limit.mu.Unlock()
			if got != 1 {
				t.Errorf("remaining concurrency = %d, want 1", got)
			}
		})
	}
}

func TestDownloadCancellationDoesNotRaceTaskQueueClose(t *testing.T) {
	data := []byte("abcdefgh")
	overloadStarted := make(chan struct{})
	releaseOverload := make(chan struct{})
	var overloadOnce sync.Once
	limit := &ConcurrencyLimit{Limit: 2}
	d := NewDownloader(func(d *Downloader) {
		d.Concurrency = 2
		d.PartSize = 4
		d.ConcurrencyLimit = limit
		d.OpenPart = func(_ context.Context, request PartRequest) (io.ReadCloser, error) {
			if request.Range.Start > 0 {
				overloadOnce.Do(func() { close(overloadStarted) })
				<-releaseOverload
				return nil, &errNeedRetry{HttpStatusCodeError(http.StatusServiceUnavailable)}
			}
			end := request.Range.Start + request.Range.Length
			return io.NopCloser(bytes.NewReader(data[request.Range.Start:end])), nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	reader, err := d.Download(ctx, int64(len(data)), http_range.Range{Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	go func() {
		_, _ = io.ReadAll(reader)
		close(readDone)
	}()
	select {
	case <-overloadStarted:
	case <-time.After(time.Second):
		t.Fatal("later range was not requested")
	}
	cancel()
	if err := reader.Close(); err != nil {
		t.Fatalf("close cancelled reader: %v", err)
	}
	close(releaseOverload)
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("cancelled reader remained blocked")
	}
	for range 100 {
		limit.mu.Lock()
		remaining := limit.Limit
		limit.mu.Unlock()
		if remaining == 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("download workers did not release concurrency slots")
}
