package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

func TestToolsListRequiresInitializedSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(map[string]*session{
		"s1": {id: "s1", userID: 1},
	})

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		srv.handlePost(c)
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":1,
		"method":"tools/list"
	}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(ProtocolVersionHeader, ProtocolVersion)
	req.Header.Set(SessionHeader, "s1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusBadRequest)
	}
	resp := decodeResponse(t, w)
	if resp.Error == nil || resp.Error.Code != -32002 {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
}

func TestToolsListSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(map[string]*session{
		"s2": {id: "s2", userID: 1, initialized: true},
	})

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		srv.handlePost(c)
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":2,
		"method":"tools/list"
	}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(ProtocolVersionHeader, ProtocolVersion)
	req.Header.Set(SessionHeader, "s2")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}
	resp := decodeResponse(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", resp.Result)
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 3 {
		t.Fatalf("unexpected tools payload: %#v", result["tools"])
	}
	names := map[string]bool{}
	for _, rawTool := range tools {
		currentTool, ok := rawTool.(map[string]any)
		if !ok {
			t.Fatalf("unexpected tool payload: %#v", rawTool)
		}
		name, _ := currentTool["name"].(string)
		names[name] = true
	}
	if !names["openlist.fs.list"] || !names["openlist.fs.get"] || !names["openlist.fs.link"] {
		t.Fatalf("unexpected tool names: %#v", names)
	}
}

func TestToolsCallUnknownTool(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(map[string]*session{
		"s3": {id: "s3", userID: 1, initialized: true},
	})

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		srv.handlePost(c)
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":3,
		"method":"tools/call",
		"params":{"name":"openlist.fs.unknown","arguments":{"path":"/"}}
	}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(ProtocolVersionHeader, ProtocolVersion)
	req.Header.Set(SessionHeader, "s3")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}
	resp := decodeResponse(t, w)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
}

func TestToolsCallInvalidParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(map[string]*session{
		"s4": {id: "s4", userID: 1, initialized: true},
	})

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		srv.handlePost(c)
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":4,
		"method":"tools/call",
		"params":{"name":"openlist.fs.list","arguments":"bad"}
	}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(ProtocolVersionHeader, ProtocolVersion)
	req.Header.Set(SessionHeader, "s4")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}
	resp := decodeResponse(t, w)
	if resp.Error != nil {
		t.Fatalf("expected tool error result, got protocol error: %+v", resp.Error)
	}
}

func decodeResponse(t *testing.T, w *httptest.ResponseRecorder) response {
	t.Helper()

	var resp response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	return resp
}
