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

func TestMCPToolsListRequiresInitializedSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	openListMCP.sessions = map[string]*mcpSession{
		"s1": {id: "s1", userID: 1},
	}

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		openListMCP.handlePost(c)
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":1,
		"method":"tools/list"
	}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(mcpSessionHeader, "s1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusBadRequest)
	}
	resp := decodeMCPResponse(t, w)
	if resp.Error == nil || resp.Error.Code != -32002 {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
}

func TestMCPToolsListSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	openListMCP.sessions = map[string]*mcpSession{
		"s2": {id: "s2", userID: 1, initialized: true},
	}

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		openListMCP.handlePost(c)
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":2,
		"method":"tools/list"
	}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(mcpSessionHeader, "s2")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}
	resp := decodeMCPResponse(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", resp.Result)
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("unexpected tools payload: %#v", result["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected tool payload: %#v", tools[0])
	}
	if tool["name"] != "openlist.fs.list" {
		t.Fatalf("unexpected tool name: got %v", tool["name"])
	}
}

func TestMCPToolsCallUnknownTool(t *testing.T) {
	gin.SetMode(gin.TestMode)
	openListMCP.sessions = map[string]*mcpSession{
		"s3": {id: "s3", userID: 1, initialized: true},
	}

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		openListMCP.handlePost(c)
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":3,
		"method":"tools/call",
		"params":{"name":"openlist.fs.unknown","arguments":{"path":"/"}}
	}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(mcpSessionHeader, "s3")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}
	resp := decodeMCPResponse(t, w)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
}

func TestMCPToolsCallInvalidParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	openListMCP.sessions = map[string]*mcpSession{
		"s4": {id: "s4", userID: 1, initialized: true},
	}

	r := gin.New()
	r.POST("/mcp", func(c *gin.Context) {
		common.GinAppendValues(c, conf.UserKey, &model.User{ID: 1, Role: model.ADMIN})
		openListMCP.handlePost(c)
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":4,
		"method":"tools/call",
		"params":{"name":"openlist.fs.list","arguments":"bad"}
	}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set(mcpSessionHeader, "s4")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", w.Code, http.StatusOK)
	}
	resp := decodeMCPResponse(t, w)
	if resp.Error != nil {
		t.Fatalf("expected tool error result, got protocol error: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", resp.Result)
	}
	if isError, ok := result["isError"].(bool); !ok || !isError {
		t.Fatalf("unexpected tool error flag: %#v", result["isError"])
	}
}

func decodeMCPResponse(t *testing.T, w *httptest.ResponseRecorder) mcpResponse {
	t.Helper()

	var resp mcpResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	return resp
}
