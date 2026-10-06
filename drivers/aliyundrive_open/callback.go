package aliyundrive_open

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	anet "github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

const (
	defaultCallbackConcurrency = 1
	callbackAcquireTimeout     = time.Second
	callbackRequestAttempts    = 3
	callbackRetryBaseDelay     = 200 * time.Millisecond
	callbackErrorBodyLimit     = 64 << 10
)

var callbackLimiters = struct {
	sync.Mutex
	byUser map[string]*callbackLimiter
}{byUser: make(map[string]*callbackLimiter)}

type callbackLimiter struct {
	userID        string
	mu            sync.Mutex
	active        int
	nextID        uint64
	registrations map[uint64]int
	changed       chan struct{}
}

type callbackRegistration struct {
	limiter *callbackLimiter
	id      uint64
	once    sync.Once
}

type callbackPermit struct {
	limiter *callbackLimiter
	once    sync.Once
}

func normalizeCallbackConcurrency(limit int) int {
	if limit <= 0 {
		return defaultCallbackConcurrency
	}
	return limit
}

func registerCallbackLimiter(userID string, limit int) *callbackRegistration {
	callbackLimiters.Lock()
	defer callbackLimiters.Unlock()

	limiter := callbackLimiters.byUser[userID]
	if limiter == nil {
		limiter = &callbackLimiter{
			userID:        userID,
			registrations: make(map[uint64]int),
			changed:       make(chan struct{}),
		}
		callbackLimiters.byUser[userID] = limiter
	}
	limiter.mu.Lock()
	limiter.nextID++
	id := limiter.nextID
	limiter.registrations[id] = normalizeCallbackConcurrency(limit)
	limiter.signalLocked()
	limiter.mu.Unlock()
	return &callbackRegistration{limiter: limiter, id: id}
}

func (r *callbackRegistration) unregister() {
	if r == nil || r.limiter == nil {
		return
	}
	r.once.Do(func() {
		callbackLimiters.Lock()
		defer callbackLimiters.Unlock()
		r.limiter.mu.Lock()
		delete(r.limiter.registrations, r.id)
		r.limiter.signalLocked()
		if len(r.limiter.registrations) == 0 && r.limiter.active == 0 {
			delete(callbackLimiters.byUser, r.limiter.userID)
		}
		r.limiter.mu.Unlock()
	})
}

func (r *callbackRegistration) acquire(ctx context.Context) (*callbackPermit, error) {
	if r == nil || r.limiter == nil {
		return nil, errs.NewErr(errs.TemporaryCapacity, "callback limiter is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, callbackAcquireTimeout)
	defer cancel()
	for {
		r.limiter.mu.Lock()
		if r.limiter.active < r.limiter.limitLocked() {
			r.limiter.active++
			r.limiter.mu.Unlock()
			return &callbackPermit{limiter: r.limiter}, nil
		}
		changed := r.limiter.changed
		r.limiter.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, errs.NewErr(errs.TemporaryCapacity, "timed out waiting for callback admission")
		case <-changed:
		}
	}
}

func (l *callbackLimiter) limitLocked() int {
	limit := 0
	for _, registered := range l.registrations {
		if limit == 0 || registered < limit {
			limit = registered
		}
	}
	return limit
}

func (l *callbackLimiter) signalLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

func (p *callbackPermit) release() {
	if p == nil || p.limiter == nil {
		return
	}
	p.once.Do(func() {
		callbackLimiters.Lock()
		defer callbackLimiters.Unlock()
		p.limiter.mu.Lock()
		p.limiter.active--
		p.limiter.signalLocked()
		if len(p.limiter.registrations) == 0 && p.limiter.active == 0 {
			delete(callbackLimiters.byUser, p.limiter.userID)
		}
		p.limiter.mu.Unlock()
	})
}

func (d *AliyundriveOpen) callbackRegistration() *callbackRegistration {
	if d.callback != nil {
		return d.callback
	}
	if d.ref != nil {
		return d.ref.callbackRegistration()
	}
	return nil
}

func (d *AliyundriveOpen) callbackRangeReader(url string, size int64) stream.RangeReaderFunc {
	return func(ctx context.Context, requested http_range.Range) (io.ReadCloser, error) {
		if requested.Length < 0 || requested.Start+requested.Length > size {
			requested.Length = size - requested.Start
		}
		for attempt := 0; attempt < callbackRequestAttempts; attempt++ {
			permit, err := d.callbackRegistration().acquire(ctx)
			if err != nil {
				return nil, err
			}
			body, retry, err := openCallbackRange(ctx, url, size, requested)
			if !retry && err == nil {
				return newCallbackBody(ctx, body, permit.release), nil
			}
			permit.release()
			if !retry {
				return nil, err
			}
			if attempt+1 == callbackRequestAttempts {
				return nil, errs.NewErr(errs.TemporaryCapacity, "Aliyun callback concurrency limit rejected %d attempts", callbackRequestAttempts)
			}
			delay := callbackRetryBaseDelay << attempt
			delay += time.Duration(rand.Int64N(int64(delay / 2)))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		return nil, errs.NewErr(errs.TemporaryCapacity, "callback attempts exhausted")
	}
}

func openCallbackRange(ctx context.Context, url string, size int64, requested http_range.Range) (io.ReadCloser, bool, error) {
	requestHeader, _ := ctx.Value(conf.RequestHeaderKey).(http.Header)
	header := anet.ProcessHeader(requestHeader, nil)
	header = http_range.ApplyRangeToHttpHeader(requested, header)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("create Aliyun callback request: %w", err)
	}
	req.Header = header
	response, err := anet.HttpClient().Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("Aliyun callback request failed: %w", err)
	}
	if response.StatusCode >= http.StatusBadRequest {
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, callbackErrorBodyLimit))
		if readErr != nil {
			return nil, false, fmt.Errorf("read Aliyun callback error response: %w", readErr)
		}
		if isCallbackCapacityRejection(response.StatusCode, body) {
			return nil, true, nil
		}
		message := strings.ReplaceAll(strings.TrimSpace(string(body)), url, "<redacted>")
		return nil, false, fmt.Errorf("Aliyun callback request failed: %w; response: %s", anet.HttpStatusCodeError(response.StatusCode), message)
	}
	if requested.Start == 0 && requested.Length == size || response.StatusCode == http.StatusPartialContent || callbackContentRangeStartsAt(response.Header, requested.Start) {
		return response.Body, false, nil
	}
	if response.StatusCode == http.StatusOK {
		body, rangeErr := anet.GetRangedHttpReader(response.Body, requested.Start, requested.Length)
		if rangeErr != nil {
			response.Body.Close()
			return nil, false, rangeErr
		}
		return body, false, nil
	}
	return response.Body, false, nil
}

func isCallbackCapacityRejection(status int, body []byte) bool {
	return status == http.StatusForbidden &&
		strings.Contains(string(body), "RequestDeniedByCallback") &&
		strings.Contains(string(body), "ExceedMaxConcurrency")
}

func callbackContentRangeStartsAt(header http.Header, offset int64) bool {
	start, _, err := http_range.ParseContentRange(header.Get("Content-Range"))
	return err == nil && start == offset
}

type callbackBody struct {
	body    io.ReadCloser
	release func()
	once    sync.Once
	mu      sync.Mutex
	stop    func() bool
}

func newCallbackBody(ctx context.Context, body io.ReadCloser, release func()) *callbackBody {
	b := &callbackBody{body: body, release: release}
	stop := context.AfterFunc(ctx, func() { _ = b.Close() })
	b.mu.Lock()
	b.stop = stop
	b.mu.Unlock()
	return b
}

func (b *callbackBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil {
		_ = b.Close()
	}
	return n, err
}

func (b *callbackBody) Close() error {
	var err error
	b.once.Do(func() {
		b.mu.Lock()
		stop := b.stop
		b.mu.Unlock()
		if stop != nil {
			stop()
		}
		err = b.body.Close()
		b.release()
	})
	return err
}
