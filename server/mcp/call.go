package mcp

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
)

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *Server) handleToolsCall(c *gin.Context, req request) (int, response) {
	var params toolCallParams
	if len(req.Params) == 0 {
		return http.StatusBadRequest, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32602, Message: "invalid tools/call params"},
		}
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		return http.StatusBadRequest, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32602, Message: "invalid tools/call params"},
		}
	}

	var (
		result any
		err    *rpcError
	)
	switch params.Name {
	case "openlist.fs.list":
		result, err = s.callFSList(c, params.Arguments)
	case "openlist.fs.get":
		result, err = s.callFSGet(c, params.Arguments)
	case "openlist.fs.link":
		result, err = s.callFSLink(c, params.Arguments)
	default:
		return http.StatusOK, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: "unknown tool"},
		}
	}

	if err != nil {
		return http.StatusOK, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []toolResultContent{
					{Type: "text", Text: err.Message},
				},
				"isError": true,
			},
		}
	}

	resultJSON, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return http.StatusInternalServerError, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32603, Message: "failed to encode tool result"},
		}
	}

	return http.StatusOK, response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"content": []toolResultContent{
				{Type: "text", Text: string(resultJSON)},
			},
			"structuredContent": result,
		},
	}
}
