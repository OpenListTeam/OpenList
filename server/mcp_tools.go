package server

import "encoding/json"

type mcpTool struct {
	Name        string             `json:"name"`
	Title       string             `json:"title,omitempty"`
	Description string             `json:"description,omitempty"`
	InputSchema mcpToolInputSchema `json:"inputSchema"`
}

type mcpToolInputSchema struct {
	Type       string                           `json:"type"`
	Properties map[string]mcpToolSchemaProperty `json:"properties,omitempty"`
	Required   []string                         `json:"required,omitempty"`
}

type mcpToolSchemaProperty struct {
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

type mcpToolsListParams struct {
	Cursor string `json:"cursor,omitempty"`
}

var openListMCPTools = []mcpTool{
	{
		Name:        "openlist.fs.list",
		Title:       "OpenList FS List",
		Description: "List files and directories under a mount path that the current user can access.",
		InputSchema: mcpToolInputSchema{
			Type: "object",
			Properties: map[string]mcpToolSchemaProperty{
				"path": {
					Type:        "string",
					Description: "Mount path to list, for example \"/\" or \"/movies\".",
				},
				"refresh": {
					Type:        "boolean",
					Description: "Refresh the directory listing before returning results.",
				},
				"password": {
					Type:        "string",
					Description: "Optional password for protected paths.",
				},
				"page": {
					Type:        "integer",
					Description: "1-based page number.",
				},
				"per_page": {
					Type:        "integer",
					Description: "Page size.",
				},
			},
			Required: []string{"path"},
		},
	},
}

func (s *mcpServer) handleToolsList(req mcpRequest) mcpResponse {
	var params mcpToolsListParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &mcpError{Code: -32602, Message: "invalid tools/list params"},
			}
		}
	}

	return mcpResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"tools": openListMCPTools,
		},
	}
}
