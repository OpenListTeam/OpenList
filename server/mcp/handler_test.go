package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

func TestInitializeCreatesSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(nil)

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		srv.handlePost(c)
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":1,
		"method":"initialize",
		"params":{
			"protocolVersion":"2025-06-18",
			"capabilities":{},
			"clientInfo":{"name":"test-client","version":"1.0.0"}
		}
	}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "http://example.com")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}
	if got := w.Header().Get(SessionHeader); got == "" {
		t.Fatal("expected session header to be set")
	}

	var resp response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
}

func TestInitializeNegotiatesUnsupportedProtocolVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(nil)

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		srv.handlePost(c)
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":1,
		"method":"initialize",
		"params":{
			"protocolVersion":"2026-01-01",
			"capabilities":{},
			"clientInfo":{"name":"test-client","version":"1.0.0"}
		}
	}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "http://example.com")

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
	if result["protocolVersion"] != ProtocolVersion {
		t.Fatalf("unexpected protocol version: %v", result["protocolVersion"])
	}
}

func TestPostInvalidAcceptReturnsJSONRPCError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(nil)

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		srv.handlePost(c)
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":1,
		"method":"initialize"
	}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "http://example.com")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotAcceptable {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusNotAcceptable)
	}
	resp := decodeResponse(t, w)
	if resp.Error == nil || resp.Error.Code != -32000 {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
}

func TestDeleteRemovesSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(nil)

	currentSession := srv.createSession(1)
	r := gin.New()
	r.DELETE("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		srv.handleDelete(c)
	})

	req := httptest.NewRequest(http.MethodDelete, "http://example.com/mcp", nil)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(SessionHeader, currentSession.id)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusNoContent)
	}
	if _, ok := srv.getSession(currentSession.id); ok {
		t.Fatal("expected session to be deleted")
	}
}

func TestGetReturnsMethodNotAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.GET("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		defaultServer.handleGet(c)
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/mcp", nil)
	req.Header.Set("Origin", "http://example.com")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusMethodNotAllowed)
	}
	if allow := w.Header().Get("Allow"); allow != "POST, DELETE" {
		t.Fatalf("unexpected Allow header: got %q want %q", allow, "POST, DELETE")
	}
}

func newTestServer(sessions map[string]*session) *Server {
	if sessions == nil {
		sessions = map[string]*session{}
	}
	now := time.Now()
	for _, currentSession := range sessions {
		if currentSession.createdAt.IsZero() {
			currentSession.createdAt = now
		}
		if currentSession.lastUsedAt.IsZero() {
			currentSession.lastUsedAt = now
		}
	}
	return &Server{sessions: sessions}
}
