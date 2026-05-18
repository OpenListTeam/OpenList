package server

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
)

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpToolResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *mcpServer) handleToolsCall(c *gin.Context, req mcpRequest) (int, mcpResponse) {
	var params mcpToolCallParams
	if len(req.Params) == 0 {
		return http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32602, Message: "invalid tools/call params"},
		}
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		return http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32602, Message: "invalid tools/call params"},
		}
	}

	var (
		result any
		err    *mcpError
	)
	switch params.Name {
	case "openlist.fs.list":
		result, err = s.callFSList(c, params.Arguments)
	default:
		return http.StatusOK, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32601, Message: "unknown tool"},
		}
	}

	if err != nil {
		return http.StatusOK, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []mcpToolResultContent{
					{Type: "text", Text: err.Message},
				},
				"isError": true,
			},
		}
	}

	resultJSON, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return http.StatusInternalServerError, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32603, Message: "failed to encode tool result"},
		}
	}

	return http.StatusOK, mcpResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"content": []mcpToolResultContent{
				{Type: "text", Text: string(resultJSON)},
			},
			"structuredContent": result,
		},
	}
}
