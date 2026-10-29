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
	// maxResumeAttempts 是同一断点连续续传的次数上限，读到数据后重置
	maxResumeAttempts = 5
	// resumeBackoff 是第 n 次续传前的等待时间基数
	resumeBackoff = 200 * time.Millisecond
)

// resumableReader 把一个一次性流包装成可续传的流。
// 上游 CDN 中途掐断连接时（unexpected EOF / 短读），用 RangeReader 从断点续读，
// 而不是让整个复制、上传任务直接失败。
type resumableReader struct {
	ctx    context.Context
	rr     model.RangeReaderIF
	size   int64
	rc     io.ReadCloser
	offset int64
	fails  int
}

func newResumableReader(ctx context.Context, rr model.RangeReaderIF, size int64, rc io.ReadCloser) *resumableReader {
	return &resumableReader{ctx: ctx, rr: rr, size: size, rc: rc}
}

func (r *resumableReader) Read(p []byte) (int, error) {
	for {
		if r.rc == nil {
			rc, err := r.rr.RangeRead(r.ctx, http_range.Range{Start: r.offset, Length: -1})
			if err != nil {
				if !r.retryable(err) {
					return 0, err
				}
				log.Warnf("stream resume request failed at %d/%d (attempt %d/%d): %v", r.offset, r.size, r.fails+1, maxResumeAttempts, err)
				r.backoff()
				continue
			}
			r.rc = rc
		}

		n, err := r.rc.Read(p)
		if n > 0 {
			r.offset += int64(n)
			r.fails = 0
			// 先把已读到的数据交出去，剩余错误留到下一次 Read 处理
			return n, nil
		}
		if err == nil {
			err = io.ErrNoProgress
		}
		// 数据已按声明长度读完，连接后续的报错不影响结果
		if r.size > 0 && r.offset >= r.size {
			return 0, io.EOF
		}
		if !r.retryable(err) {
			return 0, err
		}
		log.Warnf("stream interrupted at %d/%d (attempt %d/%d): %v, resuming", r.offset, r.size, r.fails+1, maxResumeAttempts, err)
		_ = r.rc.Close()
		r.rc = nil
		r.backoff()
	}
}

// retryable 判断当前失败是否值得从断点重开一次。
// size 未知时无法确认断点，直接放弃。
func (r *resumableReader) retryable(err error) bool {
	if r.size <= 0 || r.offset >= r.size {
		return false
	}
	if r.fails >= maxResumeAttempts || r.ctx.Err() != nil {
		return false
	}
	return true
}

func (r *resumableReader) backoff() {
	r.fails++
	select {
	case <-r.ctx.Done():
	case <-time.After(time.Duration(r.fails) * resumeBackoff):
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
