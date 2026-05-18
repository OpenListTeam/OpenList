package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/middlewares"
	"github.com/gin-gonic/gin"
)

const (
	mcpProtocolVersion = "2025-06-18"
	mcpSessionHeader   = "Mcp-Session-Id"
)

type mcpSession struct {
	id          string
	userID      uint
	initialized bool
	createdAt   time.Time
}

type mcpServer struct {
	mu       sync.RWMutex
	sessions map[string]*mcpSession
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *mcpError `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpInitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      map[string]any `json:"clientInfo"`
}

var openListMCP = &mcpServer{
	sessions: map[string]*mcpSession{},
}

func MCP(g *gin.RouterGroup) {
	mcp := g.Group("/mcp", middlewares.Auth(false), middlewares.AuthAdmin)
	mcp.GET("", openListMCP.handleGet)
	mcp.POST("", openListMCP.handlePost)
	mcp.DELETE("", openListMCP.handleDelete)
}

func (s *mcpServer) handleGet(c *gin.Context) {
	if !validateMCPOrigin(c.Request) {
		c.Status(http.StatusForbidden)
		return
	}
	c.Status(http.StatusMethodNotAllowed)
}

func (s *mcpServer) handlePost(c *gin.Context) {
	if !validateMCPOrigin(c.Request) {
		c.Status(http.StatusForbidden)
		return
	}
	if !acceptsMCPJSON(c.GetHeader("Accept")) {
		c.Status(http.StatusNotAcceptable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			Error:   &mcpError{Code: -32700, Message: "failed to read request body"},
		})
		return
	}

	var req mcpRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			Error:   &mcpError{Code: -32700, Message: "parse error"},
		})
		return
	}
	if req.JSONRPC != "2.0" {
		c.JSON(http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32600, Message: "invalid request"},
		})
		return
	}

	if req.Method == "initialize" {
		s.handleInitialize(c, req)
		return
	}

	sessionID := c.GetHeader(mcpSessionHeader)
	session, ok := s.getSession(sessionID)
	if !ok {
		c.Status(http.StatusBadRequest)
		c.JSON(http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32000, Message: "missing or invalid MCP session"},
		})
		return
	}

	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if session.userID != user.ID {
		c.Status(http.StatusNotFound)
		c.JSON(http.StatusNotFound, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32001, Message: "session not found"},
		})
		return
	}

	switch req.Method {
	case "ping":
		c.JSON(http.StatusOK, mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "notifications/initialized":
		s.markSessionInitialized(sessionID)
		c.Status(http.StatusAccepted)
	case "tools/list":
		if !s.sessionInitialized(sessionID) {
			c.JSON(http.StatusBadRequest, mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &mcpError{Code: -32002, Message: "MCP session not initialized"},
			})
			return
		}
		c.JSON(http.StatusOK, s.handleToolsList(req))
	case "tools/call":
		if !s.sessionInitialized(sessionID) {
			c.JSON(http.StatusBadRequest, mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &mcpError{Code: -32002, Message: "MCP session not initialized"},
			})
			return
		}
		status, resp := s.handleToolsCall(c, req)
		c.JSON(status, resp)
	default:
		c.JSON(http.StatusOK, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32601, Message: fmt.Sprintf("method %q not implemented yet", req.Method)},
		})
	}
}

func (s *mcpServer) handleInitialize(c *gin.Context, req mcpRequest) {
	var params mcpInitializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			c.JSON(http.StatusBadRequest, mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &mcpError{Code: -32602, Message: "invalid initialize params"},
			})
			return
		}
	}
	if params.ProtocolVersion != "" && params.ProtocolVersion != mcpProtocolVersion {
		c.JSON(http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32602, Message: "unsupported protocol version"},
		})
		return
	}

	session := s.createSession(c.Request.Context().Value(conf.UserKey).(*model.User).ID)
	c.Header(mcpSessionHeader, session.id)
	c.JSON(http.StatusOK, mcpResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{
					"listChanged": false,
				},
			},
			"serverInfo": map[string]any{
				"name":    "OpenList MCP",
				"version": conf.Version,
			},
			"instructions": "Complete initialization with notifications/initialized, then use tools/list and tools/call. The first available tool is openlist.fs.list.",
		},
	})
}

func (s *mcpServer) handleDelete(c *gin.Context) {
	if !validateMCPOrigin(c.Request) {
		c.Status(http.StatusForbidden)
		return
	}

	session, ok := s.getSession(c.GetHeader(mcpSessionHeader))
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}

	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if session.userID != user.ID {
		c.Status(http.StatusNotFound)
		return
	}

	s.deleteSession(session.id)
	c.Status(http.StatusNoContent)
}

func (s *mcpServer) createSession(userID uint) *mcpSession {
	s.mu.Lock()
	defer s.mu.Unlock()

	session := &mcpSession{
		id:        random.Token(),
		userID:    userID,
		createdAt: time.Now(),
	}
	s.sessions[session.id] = session
	return session
}

func (s *mcpServer) getSession(id string) (mcpSession, bool) {
	if id == "" {
		return mcpSession{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	if !ok || session == nil {
		return mcpSession{}, false
	}
	return *session, true
}

func (s *mcpServer) markSessionInitialized(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok || session == nil {
		return false
	}
	session.initialized = true
	return true
}

func (s *mcpServer) sessionInitialized(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	return ok && session != nil && session.initialized
}

func (s *mcpServer) deleteSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func acceptsMCPJSON(accept string) bool {
	if accept == "" {
		return false
	}
	return strings.Contains(accept, "application/json") ||
		strings.Contains(accept, "*/*")
}

func validateMCPOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	originURL, err := url.Parse(origin)
	if err != nil || originURL.Host == "" {
		return false
	}
	if strings.EqualFold(originURL.Host, r.Host) {
		return strings.EqualFold(originURL.Scheme, requestScheme(r))
	}

	siteURL := common.GetApiUrlFromRequest(r)
	if siteURL == "" {
		return false
	}
	siteParsed, err := url.Parse(siteURL)
	if err != nil {
		return false
	}
	if strings.EqualFold(originURL.Host, siteParsed.Host) && strings.EqualFold(originURL.Scheme, siteParsed.Scheme) {
		return true
	}
	return false
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "https"
	}
	return "http"
}
