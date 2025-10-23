package stream

import (
	"context"
	"io"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"golang.org/x/time/rate"
)

type Limiter interface {
	Limit() rate.Limit
	Burst() int
	TokensAt(time.Time) float64
	Tokens() float64
	Allow() bool
	AllowN(time.Time, int) bool
	Reserve() *rate.Reservation
	ReserveN(time.Time, int) *rate.Reservation
	Wait(context.Context) error
	WaitN(context.Context, int) error
	SetLimit(rate.Limit)
	SetLimitAt(time.Time, rate.Limit)
	SetBurst(int)
	SetBurstAt(time.Time, int)
}

var (
	ClientDownloadLimit Limiter
	ClientUploadLimit   Limiter
	ServerDownloadLimit Limiter
	ServerUploadLimit   Limiter
)

type RateLimitReader struct {
	io.Reader
	Limiter Limiter
	Ctx     context.Context
}

func (r *RateLimitReader) Read(p []byte) (n int, err error) {
	if err = r.Ctx.Err(); err != nil {
		return 0, err
	}
	n, err = r.Reader.Read(p)
	if err != nil {
		return
	}
	if r.Limiter != nil {
		err = r.Limiter.WaitN(r.Ctx, n)
	}
	return
}

func (r *RateLimitReader) Close() error {
	if c, ok := r.Reader.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

type RateLimitWriter struct {
	io.Writer
	Limiter Limiter
	Ctx     context.Context
}

func (w *RateLimitWriter) Write(p []byte) (n int, err error) {
	if err = w.Ctx.Err(); err != nil {
		return 0, err
	}
	n, err = w.Writer.Write(p)
	if err != nil {
		return
	}
	if w.Limiter != nil {
		err = w.Limiter.WaitN(w.Ctx, n)
	}
	return
}

func (w *RateLimitWriter) Close() error {
	if c, ok := w.Writer.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

type RateLimitFile struct {
	model.File
	Limiter Limiter
	Ctx     context.Context
}

func (r *RateLimitFile) Read(p []byte) (n int, err error) {
	if err = r.Ctx.Err(); err != nil {
		return 0, err
	}
	n, err = r.File.Read(p)
	if err != nil {
		return
	}
	if r.Limiter != nil {
		err = r.Limiter.WaitN(r.Ctx, n)
	}
	return
}

func (r *RateLimitFile) ReadAt(p []byte, off int64) (n int, err error) {
	if err = r.Ctx.Err(); err != nil {
		return 0, err
	}
	n, err = r.File.ReadAt(p, off)
	if err != nil {
		return
	}
	if r.Limiter != nil {
		err = r.Limiter.WaitN(r.Ctx, n)
	}
	return
}

func (r *RateLimitFile) Close() error {
	if c, ok := r.File.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

type RateLimitRangeReaderFunc RangeReaderFunc

func (f RateLimitRangeReaderFunc) RangeRead(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
	if ServerDownloadLimit == nil {
		return f(ctx, httpRange)
	}
	rc, err := f(ctx, httpRange)
	if err != nil {
		return nil, err
	}
	return &RateLimitReader{
		Ctx:     ctx,
		Reader:  rc,
		Limiter: ServerDownloadLimit,
	}, nil
}
