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
			"protocolVersion":"2025-11-25",
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
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", resp.Result)
	}
	if result["protocolVersion"] != ProtocolVersion {
		t.Fatalf("unexpected protocol version: %v", result["protocolVersion"])
	}
}

func TestInitializeNegotiatesSupportedOlderProtocolVersion(t *testing.T) {
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
	resp := decodeResponse(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", resp.Result)
	}
	if result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("unexpected protocol version: %v", result["protocolVersion"])
	}
}

func TestInitializeReusesExistingSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(nil)
	currentSession := srv.createSession(1)
	currentSession.initialized = true

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
	req.Header.Set(SessionHeader, currentSession.id)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}
	if got := w.Header().Get(SessionHeader); got != currentSession.id {
		t.Fatalf("unexpected session header: got %q want %q", got, currentSession.id)
	}
	if len(srv.sessions) != 1 {
		t.Fatalf("unexpected session count: got %d want %d", len(srv.sessions), 1)
	}
	reusedSession, ok := srv.getSession(currentSession.id)
	if !ok {
		t.Fatal("expected existing session to be reused")
	}
	if reusedSession.initialized {
		t.Fatal("expected reused session to require initialized notification again")
	}
	if reusedSession.protocolVersion != "2025-06-18" {
		t.Fatalf("unexpected protocol version: got %q want %q", reusedSession.protocolVersion, "2025-06-18")
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

func TestCreateSessionPrunesUserSessionLimit(t *testing.T) {
	srv := newTestServer(nil)
	var firstSessionID string
	for i := range maxUserSessions {
		currentSession := srv.createSession(1)
		if i == 0 {
			firstSessionID = currentSession.id
		}
	}
	srv.createSession(2)
	srv.createSession(1)

	if _, ok := srv.sessions[firstSessionID]; ok {
		t.Fatal("expected oldest user session to be pruned")
	}
	if got := countSessionsForUser(srv, 1); got != maxUserSessions {
		t.Fatalf("unexpected user session count: got %d want %d", got, maxUserSessions)
	}
	if got := len(srv.sessions); got != maxUserSessions+1 {
		t.Fatalf("unexpected total session count: got %d want %d", got, maxUserSessions+1)
	}
}

func TestCreateSessionPrunesGlobalSessionLimit(t *testing.T) {
	srv := newTestServer(nil)
	var firstSessionID string
	for i := range maxSessions {
		currentSession := srv.createSession(uint(i + 1))
		if i == 0 {
			firstSessionID = currentSession.id
		}
	}
	srv.createSession(uint(maxSessions + 1))

	if _, ok := srv.sessions[firstSessionID]; ok {
		t.Fatal("expected oldest global session to be pruned")
	}
	if got := len(srv.sessions); got != maxSessions {
		t.Fatalf("unexpected session count: got %d want %d", got, maxSessions)
	}
}

func TestPostAcceptsJSONOnly(t *testing.T) {
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

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}
	resp := decodeResponse(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
}

func TestPostAcceptsWildcardAccept(t *testing.T) {
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
	req.Header.Set("Accept", "*/*")
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
	req.Header.Set("Accept", "image/png")
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

func TestPostUsesNegotiatedProtocolVersionWhenHeaderMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(map[string]*session{
		"s1": {id: "s1", userID: 1, protocolVersion: "2025-06-18", initialized: true},
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
	req.Header.Set(SessionHeader, "s1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}
	resp := decodeResponse(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
}

func TestPostRejectsUnsupportedProtocolVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(map[string]*session{
		"s1": {id: "s1", userID: 1, initialized: true},
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
	req.Header.Set(ProtocolVersionHeader, "2025-03-26")
	req.Header.Set(SessionHeader, "s1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusBadRequest)
	}
	resp := decodeResponse(t, w)
	if resp.Error == nil || resp.Error.Code != -32000 {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
}

func TestPostRejectsProtocolVersionMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(map[string]*session{
		"s1": {id: "s1", userID: 1, protocolVersion: "2025-06-18", initialized: true},
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
	if resp.Error == nil || resp.Error.Code != -32000 {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
}

func TestPostRejectsMissingSession(t *testing.T) {
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
		"method":"tools/list"
	}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(ProtocolVersionHeader, ProtocolVersion)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusBadRequest)
	}
	resp := decodeResponse(t, w)
	if resp.Error == nil || resp.Error.Code != -32000 {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
}

func TestPostRejectsUnknownSessionWithNotFound(t *testing.T) {
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
		"method":"tools/list"
	}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(ProtocolVersionHeader, ProtocolVersion)
	req.Header.Set(SessionHeader, "unknown")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusNotFound)
	}
	resp := decodeResponse(t, w)
	if resp.Error == nil || resp.Error.Code != -32001 {
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

func countSessionsForUser(srv *Server, userID uint) int {
	count := 0
	for _, currentSession := range srv.sessions {
		if currentSession != nil && currentSession.userID == userID {
			count++
		}
	}
	return count
}
