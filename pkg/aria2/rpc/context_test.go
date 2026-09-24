package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Embedding Protocol and supplying Close was sufficient before context support.
// Keep this adapter assignable to Client as optional capabilities are added.
type legacyClient struct{ Protocol }

func (legacyClient) Close() error { return nil }

var _ Client = legacyClient{}

func TestHTTPContextBoundsResponseBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		// Headers arrived, then the body stalls. Cancellation must release the
		// caller even after ResponseHeaderTimeout has ceased to apply.
		cancel()
		<-req.Context().Done()
	}))
	defer server.Close()
	c, err := New(context.Background(), server.URL, "", time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.(ContextClient).WithContext(ctx).Remove("gid"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Remove() = %v", err)
	}
}

func TestWebSocketContextHandlesLateRepliesAndRPCErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upgrader := websocket.Upgrader{}
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(serverDone)
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		var first, second clientRequest
		if err := conn.ReadJSON(&first); err != nil {
			t.Error(err)
			return
		}
		cancel()
		// The second call starts after the first returned. Sending the first
		// reply now reproduces the old write into a caller-owned result.
		if err := conn.ReadJSON(&second); err != nil {
			t.Error(err)
			return
		}
		if err := conn.WriteJSON(map[string]interface{}{"jsonrpc": "2.0", "id": first.Id, "result": "late"}); err != nil {
			t.Error(err)
			return
		}
		if err := conn.WriteJSON(map[string]interface{}{"jsonrpc": "2.0", "id": second.Id, "error": map[string]interface{}{"code": 1, "message": "Active Download not found"}}); err != nil {
			t.Error(err)
			return
		}
		// Keep the session alive until the client closes it.
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	c, err := newWebsocketCaller(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var firstResult string
	if err := c.CallContext(ctx, aria2Remove, []string{"gid"}, &firstResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("first call = %v", err)
	}
	firstResult = "caller-owned"
	var secondResult string
	err = c.Call(aria2Remove, []string{"other"}, &secondResult)
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != 1 {
		t.Fatalf("second call = %v", err)
	}
	if firstResult != "caller-owned" {
		t.Fatalf("late reply overwrote result: %s", firstResult)
	}
	c.processor.mu.RLock()
	pending := len(c.processor.cbs)
	c.processor.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("pending callbacks = %d", pending)
	}
	c.Close()
	<-serverDone
}

func TestWebSocketContextDeadline(t *testing.T) {
	// A queued request with no response exercises the cleanup deadline without
	// requiring an external aria2 daemon or a sleeping server.
	c := &websocketCaller{ctx: context.Background(), timeout: time.Minute, sendChan: make(chan *sendRequest, 1), processor: NewResponseProcessor()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	var reply json.RawMessage
	if err := c.CallContext(ctx, aria2Remove, nil, &reply); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CallContext() = %v", err)
	}
	c.processor.mu.RLock()
	defer c.processor.mu.RUnlock()
	if len(c.processor.cbs) != 0 {
		t.Fatal("timed-out callback retained")
	}
}
