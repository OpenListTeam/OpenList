package mcp

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
	ProtocolVersion = "2025-06-18"
	SessionHeader   = "Mcp-Session-Id"
)

type session struct {
	id          string
	userID      uint
	initialized bool
	createdAt   time.Time
}

type Server struct {
	mu       sync.RWMutex
	sessions map[string]*session
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      map[string]any `json:"clientInfo"`
}

var defaultServer = &Server{
	sessions: map[string]*session{},
}

func Register(g *gin.RouterGroup) {
	mcpGroup := g.Group("/mcp", middlewares.Auth(false), middlewares.AuthAdmin)
	mcpGroup.GET("", defaultServer.handleGet)
	mcpGroup.POST("", defaultServer.handlePost)
	mcpGroup.DELETE("", defaultServer.handleDelete)
}

func (s *Server) handleGet(c *gin.Context) {
	if !validateOrigin(c.Request) {
		c.Status(http.StatusForbidden)
		return
	}
	c.Status(http.StatusMethodNotAllowed)
}

func (s *Server) handlePost(c *gin.Context) {
	if !validateOrigin(c.Request) {
		c.Status(http.StatusForbidden)
		return
	}
	if !acceptsJSON(c.GetHeader("Accept")) {
		c.Status(http.StatusNotAcceptable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, response{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: -32700, Message: "failed to read request body"},
		})
		return
	}

	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, response{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: -32700, Message: "parse error"},
		})
		return
	}
	if req.JSONRPC != "2.0" {
		c.JSON(http.StatusBadRequest, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32600, Message: "invalid request"},
		})
		return
	}

	if req.Method == "initialize" {
		s.handleInitialize(c, req)
		return
	}

	sessionID := c.GetHeader(SessionHeader)
	currentSession, ok := s.getSession(sessionID)
	if !ok {
		c.JSON(http.StatusBadRequest, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32000, Message: "missing or invalid MCP session"},
		})
		return
	}

	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if currentSession.userID != user.ID {
		c.JSON(http.StatusNotFound, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32001, Message: "session not found"},
		})
		return
	}

	switch req.Method {
	case "ping":
		c.JSON(http.StatusOK, response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "notifications/initialized":
		s.markSessionInitialized(sessionID)
		c.Status(http.StatusAccepted)
	case "tools/list":
		if !s.sessionInitialized(sessionID) {
			c.JSON(http.StatusBadRequest, response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32002, Message: "MCP session not initialized"},
			})
			return
		}
		c.JSON(http.StatusOK, s.handleToolsList(req))
	case "tools/call":
		if !s.sessionInitialized(sessionID) {
			c.JSON(http.StatusBadRequest, response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32002, Message: "MCP session not initialized"},
			})
			return
		}
		status, resp := s.handleToolsCall(c, req)
		c.JSON(status, resp)
	default:
		c.JSON(http.StatusOK, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: fmt.Sprintf("method %q not implemented yet", req.Method)},
		})
	}
}

func (s *Server) handleInitialize(c *gin.Context, req request) {
	var params initializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			c.JSON(http.StatusBadRequest, response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "invalid initialize params"},
			})
			return
		}
	}
	if params.ProtocolVersion != "" && params.ProtocolVersion != ProtocolVersion {
		c.JSON(http.StatusBadRequest, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32602, Message: "unsupported protocol version"},
		})
		return
	}

	currentSession := s.createSession(c.Request.Context().Value(conf.UserKey).(*model.User).ID)
	c.Header(SessionHeader, currentSession.id)
	c.JSON(http.StatusOK, response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"protocolVersion": ProtocolVersion,
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

func (s *Server) handleDelete(c *gin.Context) {
	if !validateOrigin(c.Request) {
		c.Status(http.StatusForbidden)
		return
	}

	currentSession, ok := s.getSession(c.GetHeader(SessionHeader))
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}

	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if currentSession.userID != user.ID {
		c.Status(http.StatusNotFound)
		return
	}

	s.deleteSession(currentSession.id)
	c.Status(http.StatusNoContent)
}

func (s *Server) createSession(userID uint) *session {
	s.mu.Lock()
	defer s.mu.Unlock()

	currentSession := &session{
		id:        random.Token(),
		userID:    userID,
		createdAt: time.Now(),
	}
	s.sessions[currentSession.id] = currentSession
	return currentSession
}

func (s *Server) getSession(id string) (session, bool) {
	if id == "" {
		return session{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	currentSession, ok := s.sessions[id]
	if !ok || currentSession == nil {
		return session{}, false
	}
	return *currentSession, true
}

func (s *Server) markSessionInitialized(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	currentSession, ok := s.sessions[id]
	if !ok || currentSession == nil {
		return false
	}
	currentSession.initialized = true
	return true
}

func (s *Server) sessionInitialized(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	currentSession, ok := s.sessions[id]
	return ok && currentSession != nil && currentSession.initialized
}

func (s *Server) deleteSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func acceptsJSON(accept string) bool {
	if accept == "" {
		return false
	}
	return strings.Contains(accept, "application/json") || strings.Contains(accept, "*/*")
}

func validateOrigin(r *http.Request) bool {
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
	return strings.EqualFold(originURL.Host, siteParsed.Host) && strings.EqualFold(originURL.Scheme, siteParsed.Scheme)
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "https"
	}
	return "http"
}
