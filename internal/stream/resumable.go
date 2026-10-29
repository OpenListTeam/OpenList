package stream

import (
	"context"
	"io"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	log "github.com/sirupsen/logrus"
)

const (
	maxResumeAttempts = 5
	resumeBackoff     = 200 * time.Millisecond
)

type resumableReader struct {
	ctx    context.Context
	rr     model.RangeReaderIF
	rc     io.ReadCloser
	offset int64
	err    error
}

func (r *resumableReader) Read(p []byte) (int, error) {
	for attempts := 0; ; {
		if r.err != nil {
			if attempts == maxResumeAttempts {
				if r.err == io.EOF {
					return 0, io.ErrUnexpectedEOF
				}
				return 0, r.err
			}
			if r.rc != nil {
				_ = r.rc.Close()
				r.rc = nil
			}
			attempts++
			log.Warnf("[resumable.go]stream interrupted at %d (attempt %d/%d): %v, resuming", r.offset, attempts, maxResumeAttempts, r.err)
			select {
			case <-r.ctx.Done():
				return 0, r.ctx.Err()
			case <-time.After(time.Duration(attempts) * resumeBackoff):
			}
			r.rc, r.err = r.rr.RangeRead(r.ctx, http_range.Range{Start: r.offset, Length: -1})
			if r.err != nil {
				continue
			}
		}
		n, err := r.rc.Read(p)
		r.offset += int64(n)
		r.err = err
		if n > 0 || err == nil {
			return n, nil
		}
	}
}

func (r *resumableReader) Close() error {
	if r.rc == nil {
		return nil
	}
	err := r.rc.Close()
	r.rc = nil
	return err
}
