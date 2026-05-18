package server

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

func TestMCPInitializeCreatesSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	openListMCP.sessions = map[string]*mcpSession{}

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		openListMCP.handlePost(c)
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
	if got := w.Header().Get(mcpSessionHeader); got == "" {
		t.Fatal("expected session header to be set")
	}

	var resp mcpResponse
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
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Fatalf("unexpected protocol version: got %v want %s", result["protocolVersion"], mcpProtocolVersion)
	}
}

func TestMCPDeleteRemovesSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	openListMCP.sessions = map[string]*mcpSession{}

	session := openListMCP.createSession(1)
	r := gin.New()
	r.DELETE("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		openListMCP.handleDelete(c)
	})

	req := httptest.NewRequest(http.MethodDelete, "http://example.com/mcp", nil)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(mcpSessionHeader, session.id)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusNoContent)
	}
	if _, ok := openListMCP.getSession(session.id); ok {
		t.Fatal("expected session to be deleted")
	}
}

func TestMCPGetReturnsMethodNotAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.GET("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		openListMCP.handleGet(c)
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/mcp", nil)
	req.Header.Set("Origin", "http://example.com")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusMethodNotAllowed)
	}
}
