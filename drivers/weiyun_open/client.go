package weiyun_open

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/go-resty/resty/v2"
)

const (
	jsonRPCVersion = "2.0"
	toolCallMethod = "tools/call"
)

type mcpClient struct {
	apiURL string
	envID  string
	token  string
	http   *resty.Client
}

type rpcRequest struct {
	Version string    `json:"jsonrpc"`
	ID      int64     `json:"id"`
	Method  string    `json:"method"`
	Params  rpcParams `json:"params"`
}

type rpcParams struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

type rpcResponse struct {
	Error  *rpcError `json:"error"`
	Result rpcResult `json:"result"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResult struct {
	Content []rpcContent `json:"content"`
}

type rpcContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func newMCPClient(addition Addition) *mcpClient {
	apiURL := addition.APIURL
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	return &mcpClient{
		apiURL: apiURL,
		envID:  addition.EnvID,
		token:  addition.MCPToken,
		http:   base.NewRestyClient(),
	}
}

func (c *mcpClient) call(ctx context.Context, name string, args any, out any) error {
	reqBody := rpcRequest{
		Version: jsonRPCVersion,
		ID:      time.Now().UnixNano(),
		Method:  toolCallMethod,
		Params: rpcParams{
			Name:      name,
			Arguments: args,
		},
	}
	resp, err := c.http.R().
		SetContext(ctx).
		SetHeaders(c.headers()).
		SetBody(&reqBody).
		Post(c.apiURL)
	if err != nil {
		return err
	}
	if resp.IsError() {
		return fmt.Errorf("weiyun mcp http %d: %s", resp.StatusCode(), trimBody(resp.String()))
	}
	var rpcResp rpcResponse
	if err = json.Unmarshal(resp.Body(), &rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("weiyun mcp rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	text, err := rpcResp.Result.text()
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(text), out)
}

func (c *mcpClient) headers() map[string]string {
	headers := map[string]string{
		"Content-Type": "application/json",
		"WyHeader":     "mcp_token=" + c.token,
	}
	if c.envID != "" {
		headers["Cookie"] = "env_id=" + c.envID
	}
	return headers
}

func (r rpcResult) text() (string, error) {
	for _, item := range r.Content {
		if item.Type == "text" {
			return item.Text, nil
		}
	}
	return "", fmt.Errorf("weiyun mcp response missing text content")
}

func trimBody(body string) string {
	body = strings.TrimSpace(body)
	if len(body) <= 200 {
		return body
	}
	return body[:200]
}
