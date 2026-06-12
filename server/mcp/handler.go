package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
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
	ProtocolVersion       = "2025-11-25"
	ProtocolVersionHeader = "MCP-Protocol-Version"
	SessionHeader         = "MCP-Session-Id"
	sessionTTL            = 30 * time.Minute
	maxSessions           = 128
	maxUserSessions       = 16
)

type session struct {
	id              string
	userID          uint
	protocolVersion string
	initialized     bool
	createdAt       time.Time
	lastUsedAt      time.Time
}

type Server struct {
	mu       sync.Mutex
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

var supportedProtocolVersions = map[string]struct{}{
	"2025-11-25": {},
	"2025-06-18": {},
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
	c.Header("Allow", "POST, DELETE")
	c.Status(http.StatusMethodNotAllowed)
}

func (s *Server) handlePost(c *gin.Context) {
	if !validateOrigin(c.Request) {
		c.Status(http.StatusForbidden)
		return
	}
	if !acceptsStreamableHTTP(c.GetHeader("Accept")) {
		c.JSON(http.StatusNotAcceptable, response{
			JSONRPC: "2.0",
			Error: &rpcError{
				Code:    -32000,
				Message: "Not Acceptable: client must accept both application/json and text/event-stream",
			},
		})
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
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32000, Message: "missing MCP session"},
		})
		return
	}

	currentSession, ok := s.getSession(sessionID)
	if !ok {
		c.JSON(http.StatusNotFound, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32001, Message: "session not found"},
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

	if !s.validateRequestProtocolVersion(c.GetHeader(ProtocolVersionHeader), currentSession.protocolVersion) {
		c.JSON(http.StatusBadRequest, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32000, Message: "missing or unsupported MCP protocol version"},
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

	protocolVersion := negotiateProtocolVersion(params.ProtocolVersion)
	currentSession := s.initializeSession(
		c.Request.Context().Value(conf.UserKey).(*model.User).ID,
		c.GetHeader(SessionHeader),
		protocolVersion,
	)
	c.Header(SessionHeader, currentSession.id)
	c.JSON(http.StatusOK, response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{
					"listChanged": false,
				},
			},
			"serverInfo": map[string]any{
				"name":    "OpenList MCP",
				"version": conf.Version,
			},
			"instructions": "Complete initialization with notifications/initialized, then use tools/list and tools/call. Available tools include openlist.fs.list, openlist.fs.get, and openlist.fs.link.",
		},
	})
}

func (s *Server) initializeSession(userID uint, requestedID string, protocolVersion string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.pruneExpiredSessionsLocked(now)
	if requestedID != "" {
		currentSession, ok := s.sessions[requestedID]
		if ok && currentSession != nil && currentSession.userID == userID {
			currentSession.initialized = false
			currentSession.protocolVersion = protocolVersion
			currentSession.lastUsedAt = now
			return currentSession
		}
	}

	return s.createSessionLocked(userID, protocolVersion, now)
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

	now := time.Now()
	s.pruneExpiredSessionsLocked(now)
	return s.createSessionLocked(userID, ProtocolVersion, now)
}

func (s *Server) createSessionLocked(userID uint, protocolVersion string, now time.Time) *session {
	s.pruneLeastRecentlyUsedUserSessionsLocked(userID, max(0, s.countUserSessionsLocked(userID)-maxUserSessions+1))
	s.pruneLeastRecentlyUsedSessionsLocked(max(0, len(s.sessions)-maxSessions+1))
	currentSession := &session{
		id:              random.Token(),
		userID:          userID,
		protocolVersion: protocolVersion,
		createdAt:       now,
		lastUsedAt:      now,
	}
	s.sessions[currentSession.id] = currentSession
	return currentSession
}

func (s *Server) validateRequestProtocolVersion(requestedVersion string, negotiatedVersion string) bool {
	if requestedVersion == "" {
		_, ok := supportedProtocolVersions[negotiatedVersion]
		return ok
	}
	if _, ok := supportedProtocolVersions[requestedVersion]; !ok {
		return false
	}
	return negotiatedVersion == "" || requestedVersion == negotiatedVersion
}

func negotiateProtocolVersion(requestedVersion string) string {
	if _, ok := supportedProtocolVersions[requestedVersion]; ok {
		return requestedVersion
	}
	return ProtocolVersion
}

func (s *Server) getSession(id string) (session, bool) {
	if id == "" {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	currentSession, ok := s.sessions[id]
	if !ok || currentSession == nil {
		return session{}, false
	}
	now := time.Now()
	if sessionExpired(currentSession, now) {
		delete(s.sessions, id)
		return session{}, false
	}
	currentSession.lastUsedAt = now
	return *currentSession, true
}

func (s *Server) markSessionInitialized(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	currentSession, ok := s.sessions[id]
	if !ok || currentSession == nil {
		return false
	}
	now := time.Now()
	if sessionExpired(currentSession, now) {
		delete(s.sessions, id)
		return false
	}
	currentSession.initialized = true
	currentSession.lastUsedAt = now
	return true
}

func (s *Server) sessionInitialized(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	currentSession, ok := s.sessions[id]
	if !ok || currentSession == nil {
		return false
	}
	now := time.Now()
	if sessionExpired(currentSession, now) {
		delete(s.sessions, id)
		return false
	}
	currentSession.lastUsedAt = now
	return currentSession.initialized
}

func (s *Server) deleteSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *Server) pruneExpiredSessionsLocked(now time.Time) {
	for id, currentSession := range s.sessions {
		if currentSession == nil || sessionExpired(currentSession, now) {
			delete(s.sessions, id)
		}
	}
}

func (s *Server) pruneLeastRecentlyUsedSessionsLocked(count int) {
	for range count {
		var (
			oldestID string
			oldest   time.Time
		)
		for id, currentSession := range s.sessions {
			if currentSession == nil {
				oldestID = id
				break
			}
			lastUsedAt := sessionLastUsedAt(currentSession)
			if oldestID == "" || lastUsedAt.Before(oldest) {
				oldestID = id
				oldest = lastUsedAt
			}
		}
		if oldestID == "" {
			return
		}
		delete(s.sessions, oldestID)
	}
}

func (s *Server) countUserSessionsLocked(userID uint) int {
	count := 0
	for _, currentSession := range s.sessions {
		if currentSession != nil && currentSession.userID == userID {
			count++
		}
	}
	return count
}

func (s *Server) pruneLeastRecentlyUsedUserSessionsLocked(userID uint, count int) {
	for range count {
		var (
			oldestID string
			oldest   time.Time
		)
		for id, currentSession := range s.sessions {
			if currentSession == nil {
				continue
			}
			if currentSession.userID != userID {
				continue
			}
			lastUsedAt := sessionLastUsedAt(currentSession)
			if oldestID == "" || lastUsedAt.Before(oldest) {
				oldestID = id
				oldest = lastUsedAt
			}
		}
		if oldestID == "" {
			return
		}
		delete(s.sessions, oldestID)
	}
}

func sessionExpired(currentSession *session, now time.Time) bool {
	lastUsedAt := sessionLastUsedAt(currentSession)
	return !lastUsedAt.IsZero() && now.Sub(lastUsedAt) > sessionTTL
}

func sessionLastUsedAt(currentSession *session) time.Time {
	if currentSession.lastUsedAt.IsZero() {
		return currentSession.createdAt
	}
	return currentSession.lastUsedAt
}

func acceptsStreamableHTTP(accept string) bool {
	if accept == "" {
		return false
	}
	hasJSON := false
	hasSSE := false
	for part := range strings.SplitSeq(accept, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		if q, ok := params["q"]; ok {
			quality, err := strconv.ParseFloat(q, 64)
			if err == nil && quality == 0 {
				continue
			}
		}
		switch mediaType {
		case "*/*":
			hasJSON = true
			hasSSE = true
		case "application/*", "application/json":
			hasJSON = true
		case "text/*", "text/event-stream":
			hasSSE = true
		}
	}
	return hasJSON || hasSSE
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
