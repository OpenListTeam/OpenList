package aliyundrive_open

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	anet "github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

func TestProxyLinkLimitsActiveCallbackBodies(t *testing.T) {
	oldConf := conf.Conf
	conf.Conf = &conf.Config{}
	t.Cleanup(func() { conf.Conf = oldConf })
	base.InitClient()

	started := make(chan struct{}, 2)
	transport := &callbackTrackingTransport{started: started}
	client := anet.HttpClient()
	oldTransport := client.Transport
	client.Transport = transport
	t.Cleanup(func() { client.Transport = oldTransport })

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/adrive/v1.0/user/getDriveInfo":
			_, _ = fmt.Fprint(w, `{"user_id":"user-1","resource_drive_id":"drive-1"}`)
		case "/adrive/v1.0/openFile/getDownloadUrl":
			_, _ = fmt.Fprintf(w, `{"url":%q}`, server.URL+"/callback")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldAPIURL := API_URL
	API_URL = server.URL
	defer func() { API_URL = oldAPIURL }()

	d := &AliyundriveOpen{Addition: Addition{AccessToken: "token"}}
	if err := d.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer d.Drop(context.Background())
	if d.CallbackConcurrency != defaultCallbackConcurrency {
		t.Fatalf("normalized callback concurrency = %d, want %d", d.CallbackConcurrency, defaultCallbackConcurrency)
	}

	link, err := d.Link(t.Context(), &model.Object{ID: "file-1", Name: "file", Size: 1}, model.LinkArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if link.RangeReader == nil {
		t.Fatal("proxy link must own callback acquisition through a range reader")
	}
	if _, ok := link.RangeReader.(stream.RateLimitRangeReaderFunc); !ok {
		t.Fatalf("proxy range reader type = %T, want server-rate-limited reader", link.RangeReader)
	}
	direct, err := d.Link(t.Context(), &model.Object{ID: "file-1", Name: "file", Size: 1}, model.LinkArgs{Redirect: true})
	if err != nil {
		t.Fatal(err)
	}
	if direct.URL == "" || direct.RangeReader != nil {
		t.Fatal("redirect link must remain URL-only")
	}

	first, err := link.RangeReader.RangeRead(t.Context(), http_range.Range{Length: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first callback request did not start")
	}

	type result struct {
		body interface{ Close() error }
		err  error
	}
	secondResult := make(chan result, 1)
	go func() {
		body, readErr := link.RangeReader.RangeRead(t.Context(), http_range.Range{Length: 1})
		secondResult <- result{body: body, err: readErr}
	}()

	select {
	case <-started:
		t.Fatal("second callback started before the first body was closed")
	case <-time.After(100 * time.Millisecond):
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	var second result
	select {
	case second = <-secondResult:
	case <-time.After(time.Second):
		t.Fatal("second callback did not start after the first body was closed")
	}
	if second.err != nil {
		t.Fatal(second.err)
	}
	if err := second.body.Close(); err != nil {
		t.Fatal(err)
	}
	if got := transport.peak.Load(); got != 1 {
		t.Fatalf("peak active callback bodies = %d, want 1", got)
	}
}

type callbackTrackingTransport struct {
	active  atomic.Int32
	peak    atomic.Int32
	started chan<- struct{}
}

func (t *callbackTrackingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	current := t.active.Add(1)
	for {
		old := t.peak.Load()
		if current <= old || t.peak.CompareAndSwap(old, current) {
			break
		}
	}
	t.started <- struct{}{}
	return &http.Response{
		StatusCode:    http.StatusPartialContent,
		Header:        http.Header{"Content-Length": {"1"}, "Content-Range": {"bytes 0-0/1"}},
		Body:          &trackedCallbackBody{Reader: strings.NewReader("x"), close: func() { t.active.Add(-1) }},
		ContentLength: 1,
		Request:       request,
	}, nil
}

type trackedCallbackBody struct {
	io.Reader
	once  sync.Once
	close func()
}

func (b *trackedCallbackBody) Close() error {
	b.once.Do(b.close)
	return nil
}

func TestCallbackLimiterUsesMinimumRegisteredLimit(t *testing.T) {
	firstRegistration := registerCallbackLimiter(t.Name(), 2)
	t.Cleanup(firstRegistration.unregister)
	first, err := firstRegistration.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := firstRegistration.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	defer second.release()

	lowerRegistration := registerCallbackLimiter(t.Name(), 1)
	t.Cleanup(lowerRegistration.unregister)
	acquired := make(chan *callbackPermit, 1)
	go func() {
		permit, acquireErr := lowerRegistration.acquire(t.Context())
		if acquireErr == nil {
			acquired <- permit
		}
	}()

	first.release()
	select {
	case permit := <-acquired:
		permit.release()
		t.Fatal("lowering the shared limit must wait for all excess bodies to drain")
	case <-time.After(100 * time.Millisecond):
	}
	second.release()
	select {
	case permit := <-acquired:
		permit.release()
	case <-time.After(time.Second):
		t.Fatal("admission did not resume after active bodies drained below the new limit")
	}
}

func TestCallbackLimiterSeparatesUsers(t *testing.T) {
	firstUser := registerCallbackLimiter(t.Name()+"-first", 1)
	secondUser := registerCallbackLimiter(t.Name()+"-second", 1)
	t.Cleanup(firstUser.unregister)
	t.Cleanup(secondUser.unregister)
	first, err := firstUser.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	second, err := secondUser.acquire(t.Context())
	if err != nil {
		t.Fatalf("independent user was blocked: %v", err)
	}
	second.release()
}

func TestCallbackLimiterReconfigureWaitsForOldBodies(t *testing.T) {
	userID := t.Name()
	oldRegistration := registerCallbackLimiter(userID, 2)
	first, err := oldRegistration.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := oldRegistration.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	oldRegistration.unregister()

	newRegistration := registerCallbackLimiter(userID, 1)
	t.Cleanup(newRegistration.unregister)
	acquired := make(chan *callbackPermit, 1)
	go func() {
		permit, acquireErr := newRegistration.acquire(t.Context())
		if acquireErr == nil {
			acquired <- permit
		}
	}()
	first.release()
	select {
	case permit := <-acquired:
		permit.release()
		t.Fatal("reconfigured limiter admitted while an old body still occupied the new limit")
	case <-time.After(100 * time.Millisecond):
	}
	second.release()
	select {
	case permit := <-acquired:
		permit.release()
	case <-time.After(time.Second):
		t.Fatal("reconfigured limiter did not admit after old bodies drained")
	}
}

func TestCallbackLimiterDistinguishesTimeoutAndCancellation(t *testing.T) {
	registration := registerCallbackLimiter(t.Name(), 1)
	t.Cleanup(registration.unregister)
	permit, err := registration.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer permit.release()

	started := time.Now()
	_, err = registration.acquire(t.Context())
	if !errors.Is(err, errs.TemporaryCapacity) {
		t.Fatalf("admission timeout error = %v, want TemporaryCapacity", err)
	}
	if time.Since(started) < callbackAcquireTimeout {
		t.Fatal("admission timed out before the configured wait elapsed")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = registration.acquire(ctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, errs.TemporaryCapacity) {
		t.Fatalf("canceled admission error = %v, want only context.Canceled", err)
	}
}

func TestCallbackCapacityRejectionRequiresBothExactMarkers(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "both", body: `{"code":"RequestDeniedByCallback","message":"ExceedMaxConcurrency"}`, want: true},
		{name: "code only", body: `{"code":"RequestDeniedByCallback"}`},
		{name: "message only", body: `{"message":"ExceedMaxConcurrency"}`},
		{name: "case differs", body: `{"code":"requestdeniedbycallback","message":"ExceedMaxConcurrency"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isCallbackCapacityRejection(http.StatusForbidden, []byte(test.body)); got != test.want {
				t.Fatalf("classification = %v, want %v", got, test.want)
			}
		})
	}
	if isCallbackCapacityRejection(http.StatusTooManyRequests, []byte(`RequestDeniedByCallback ExceedMaxConcurrency`)) {
		t.Fatal("non-403 response must not be classified as callback capacity")
	}
}

func TestCallbackRangeRetriesOnlyVerifiedCapacityRejections(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"RequestDeniedByCallback","message":"ExceedMaxConcurrency"}`)
	}))
	defer server.Close()

	registration := registerCallbackLimiter(t.Name(), 1)
	t.Cleanup(registration.unregister)
	d := &AliyundriveOpen{callback: registration}
	_, err := d.callbackRangeReader(server.URL+"?token=secret", 1).RangeRead(t.Context(), http_range.Range{Length: 1})
	if !errors.Is(err, errs.TemporaryCapacity) {
		t.Fatalf("verified rejection error = %v, want TemporaryCapacity", err)
	}
	if requests.Load() != callbackRequestAttempts {
		t.Fatalf("requests = %d, want %d", requests.Load(), callbackRequestAttempts)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("capacity error leaked the signed callback URL")
	}
	permit, acquireErr := registration.acquire(t.Context())
	if acquireErr != nil {
		t.Fatalf("capacity retries leaked admission: %v", acquireErr)
	}
	permit.release()

	requests.Store(0)
	permanent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"RequestDeniedByCallback","message":"denied"}`)
	}))
	defer permanent.Close()
	_, err = d.callbackRangeReader(permanent.URL+"?token=secret", 1).RangeRead(t.Context(), http_range.Range{Length: 1})
	if errors.Is(err, errs.TemporaryCapacity) {
		t.Fatalf("permanent 403 error = %v, must not be TemporaryCapacity", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("permanent 403 requests = %d, want 1", requests.Load())
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("permanent error leaked the signed callback URL")
	}
}

type countingReadCloser struct {
	reader io.Reader
	closed atomic.Int32
}

func (r *countingReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }
func (r *countingReadCloser) Close() error {
	r.closed.Add(1)
	return nil
}

func TestCallbackBodyReleasesExactlyOnce(t *testing.T) {
	underlying := &countingReadCloser{reader: strings.NewReader("x")}
	var released atomic.Int32
	body := newCallbackBody(t.Context(), underlying, func() { released.Add(1) })
	_, _ = io.ReadAll(body)
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if underlying.closed.Load() != 1 || released.Load() != 1 {
		t.Fatalf("close count = %d, release count = %d; want 1, 1", underlying.closed.Load(), released.Load())
	}
}

type failingReadCloser struct {
	closed atomic.Int32
}

func (*failingReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (r *failingReadCloser) Close() error {
	r.closed.Add(1)
	return nil
}

func TestCallbackBodyReadFailureReleasesPermit(t *testing.T) {
	underlying := &failingReadCloser{}
	var released atomic.Int32
	body := newCallbackBody(t.Context(), underlying, func() { released.Add(1) })
	if _, err := body.Read(make([]byte, 1)); err == nil {
		t.Fatal("read unexpectedly succeeded")
	}
	if underlying.closed.Load() != 1 || released.Load() != 1 {
		t.Fatalf("close count = %d, release count = %d; want 1, 1", underlying.closed.Load(), released.Load())
	}
}

func TestCallbackBodyCancellationReleasesPermit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	underlying := &countingReadCloser{reader: strings.NewReader("x")}
	released := make(chan struct{}, 1)
	_ = newCallbackBody(ctx, underlying, func() { released <- struct{}{} })
	cancel()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not release callback admission")
	}
	if underlying.closed.Load() != 1 {
		t.Fatalf("underlying close count = %d, want 1", underlying.closed.Load())
	}
}
