package mcp

import "encoding/json"

type tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema toolInputSchema `json:"inputSchema"`
}

type toolInputSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]schemaProperty `json:"properties,omitempty"`
	Required   []string                  `json:"required,omitempty"`
}

type schemaProperty struct {
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

type toolsListParams struct {
	Cursor string `json:"cursor,omitempty"`
}

var openListTools = []tool{
	{
		Name:        "openlist.fs.list",
		Title:       "OpenList FS List",
		Description: "List files and directories under a mount path that the current user can access.",
		InputSchema: toolInputSchema{
			Type: "object",
			Properties: map[string]schemaProperty{
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
	{
		Name:        "openlist.fs.get",
		Title:       "OpenList FS Get",
		Description: "Get file or directory details for a mount path that the current user can access.",
		InputSchema: toolInputSchema{
			Type: "object",
			Properties: map[string]schemaProperty{
				"path": {
					Type:        "string",
					Description: "Mount path to inspect, for example \"/movies/demo.mp4\".",
				},
				"password": {
					Type:        "string",
					Description: "Optional password for protected paths.",
				},
			},
			Required: []string{"path"},
		},
	},
	{
		Name:        "openlist.fs.link",
		Title:       "OpenList FS Link",
		Description: "Return usable link information for a file path that the current user can access.",
		InputSchema: toolInputSchema{
			Type: "object",
			Properties: map[string]schemaProperty{
				"path": {
					Type:        "string",
					Description: "File mount path, for example \"/movies/demo.mp4\".",
				},
				"password": {
					Type:        "string",
					Description: "Optional password for protected paths.",
				},
				"type": {
					Type:        "string",
					Description: "Optional link type forwarded to storage drivers.",
				},
			},
			Required: []string{"path"},
		},
	},
}

func (s *Server) handleToolsList(req request) response {
	var params toolsListParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "invalid tools/list params"},
			}
		}
	}

	return response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"tools": openListTools,
		},
	}
}
