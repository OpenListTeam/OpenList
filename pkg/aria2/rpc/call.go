package rpc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

type caller interface {
	// Call sends a request of rpc to aria2 daemon
	Call(method string, params, reply interface{}) (err error)
	CallContext(ctx context.Context, method string, params, reply interface{}) error
	Close() error
}

type httpCaller struct {
	uri    string
	c      *http.Client
	cancel context.CancelFunc
	wg     *sync.WaitGroup
	once   sync.Once
}

func newHTTPCaller(ctx context.Context, u *url.URL, timeout time.Duration, notifier Notifier) *httpCaller {
	c := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 1,
			MaxConnsPerHost:     1,
			// TLSClientConfig:     tlsConfig,
			Dial: (&net.Dialer{
				Timeout:   timeout,
				KeepAlive: 60 * time.Second,
			}).Dial,
			TLSHandshakeTimeout:   3 * time.Second,
			ResponseHeaderTimeout: timeout,
		},
	}
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	h := &httpCaller{uri: u.String(), c: c, cancel: cancel, wg: &wg}
	if notifier != nil {
		h.setNotifier(ctx, *u, notifier)
	}
	return h
}

func (h *httpCaller) Close() (err error) {
	h.once.Do(func() {
		h.cancel()
		h.wg.Wait()
	})
	return
}

func (h *httpCaller) setNotifier(ctx context.Context, u url.URL, notifier Notifier) (err error) {
	u.Scheme = "ws"
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer conn.Close()
		<-ctx.Done()
		conn.SetWriteDeadline(time.Now().Add(time.Second))
		if err := conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
			log.Printf("sending websocket close message: %v", err)
		}
	}()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		var request websocketResponse
		var err error
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if err = conn.ReadJSON(&request); err != nil {
				select {
				case <-ctx.Done():
					return
				default:
				}
				log.Printf("conn.ReadJSON|err:%v", err.Error())
				return
			}
			switch request.Method {
			case "aria2.onDownloadStart":
				notifier.OnDownloadStart(request.Params)
			case "aria2.onDownloadPause":
				notifier.OnDownloadPause(request.Params)
			case "aria2.onDownloadStop":
				notifier.OnDownloadStop(request.Params)
			case "aria2.onDownloadComplete":
				notifier.OnDownloadComplete(request.Params)
			case "aria2.onDownloadError":
				notifier.OnDownloadError(request.Params)
			case "aria2.onBtDownloadComplete":
				notifier.OnBtDownloadComplete(request.Params)
			default:
				log.Printf("unexpected notification: %s", request.Method)
			}
		}
	}()
	return
}

func (h *httpCaller) Call(method string, params, reply interface{}) (err error) {
	return h.CallContext(context.Background(), method, params, reply)
}

func (h *httpCaller) CallContext(ctx context.Context, method string, params, reply interface{}) (err error) {
	payload, err := EncodeClientRequest(method, params)
	if err != nil {
		return
	}
	// The request context also bounds reading the response body; the transport's
	// ResponseHeaderTimeout only covers receiving headers.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.uri, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	r, err := h.c.Do(req)
	if err != nil {
		return
	}
	err = DecodeClientResponse(r.Body, &reply)
	r.Body.Close()
	return
}

type websocketCaller struct {
	conn      *websocket.Conn
	sendChan  chan *sendRequest
	cancel    context.CancelFunc
	wg        *sync.WaitGroup
	once      sync.Once
	timeout   time.Duration
	ctx       context.Context
	processor *ResponseProcessor
}

func newWebsocketCaller(ctx context.Context, uri string, timeout time.Duration, notifier Notifier) (*websocketCaller, error) {
	var header = http.Header{}
	conn, _, err := websocket.DefaultDialer.Dial(uri, header)
	if err != nil {
		return nil, err
	}

	sendChan := make(chan *sendRequest, 16)
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	processor := NewResponseProcessor()
	w := &websocketCaller{conn: conn, wg: &wg, cancel: cancel, sendChan: sendChan, timeout: timeout, ctx: ctx, processor: processor}
	wg.Add(1)
	go func() { // routine:recv
		defer wg.Done()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			var resp websocketResponse
			if err := conn.ReadJSON(&resp); err != nil {
				select {
				case <-ctx.Done():
					return
				default:
				}
				log.Printf("conn.ReadJSON|err:%v", err.Error())
				return
			}
			if resp.Id == nil { // RPC notifications
				if notifier != nil {
					switch resp.Method {
					case "aria2.onDownloadStart":
						notifier.OnDownloadStart(resp.Params)
					case "aria2.onDownloadPause":
						notifier.OnDownloadPause(resp.Params)
					case "aria2.onDownloadStop":
						notifier.OnDownloadStop(resp.Params)
					case "aria2.onDownloadComplete":
						notifier.OnDownloadComplete(resp.Params)
					case "aria2.onDownloadError":
						notifier.OnDownloadError(resp.Params)
					case "aria2.onBtDownloadComplete":
						notifier.OnBtDownloadComplete(resp.Params)
					default:
						log.Printf("unexpected notification: %s", resp.Method)
					}
				}
				continue
			}
			processor.Process(resp.clientResponse)
		}
	}()
	wg.Add(1)
	go func() { // routine:send
		defer wg.Done()
		defer cancel()
		defer w.conn.Close()

		for {
			select {
			case <-ctx.Done():
				w.conn.SetWriteDeadline(time.Now().Add(timeout))
				if err := w.conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
					log.Printf("sending websocket close message: %v", err)
				}
				return
			case req := <-sendChan:
				if req.ctx.Err() != nil {
					continue
				}
				deadline, _ := req.ctx.Deadline()
				w.conn.SetWriteDeadline(deadline)
				if err := w.conn.WriteJSON(req.request); err != nil {
					req.writeErr <- err
					return
				}
			}
		}
	}()

	return w, nil
}

func (w *websocketCaller) Close() (err error) {
	w.once.Do(func() {
		w.cancel()
		w.wg.Wait()
	})
	return
}

func (w *websocketCaller) Call(method string, params, reply interface{}) (err error) {
	return w.CallContext(context.Background(), method, params, reply)
}

func (w *websocketCaller) CallContext(parent context.Context, method string, params, reply interface{}) error {
	ctx, cancel := context.WithTimeout(parent, w.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	id := reqid()
	responses := make(chan clientResponse, 1)
	writeErr := make(chan error, 1)
	// Decode on the calling goroutine. A late response after timeout must only
	// touch this buffered channel, since the caller has already regained reply.
	w.processor.Add(id, func(resp clientResponse) error {
		select {
		case responses <- resp:
		default:
		}
		return nil
	})
	defer w.processor.remove(id)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		return w.ctx.Err()
	case w.sendChan <- &sendRequest{ctx: ctx, writeErr: writeErr, request: &clientRequest{
		Version: "2.0",
		Method:  method,
		Params:  params,
		Id:      id,
	}}:

	default:
		return errors.New("sending channel blocking")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		return w.ctx.Err()
	case err := <-writeErr:
		return err
	case resp := <-responses:
		return resp.decode(reply)
	}
}

type sendRequest struct {
	ctx      context.Context
	request  *clientRequest
	writeErr chan error
}

var reqid = func() func() uint64 {
	var id atomic.Uint64
	id.Store(uint64(time.Now().UnixNano()))
	return func() uint64 {
		return id.Add(1)
	}
}()
