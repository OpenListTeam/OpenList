package pds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultClientID = "lMNVp25Sd1MfqZDQ"
	apiEndpoint     = "https://%s.api.aliyunfile.com"
	authEndpoint    = "https://%s.auth.aliyunfile.com"
)

type client struct {
	addition *Addition
	http     *http.Client
	onSave   func()
}

func newClient(addition *Addition, onSave func()) *client {
	if addition.ClientID == "" {
		addition.ClientID = defaultClientID
	}
	if addition.TokenType == "" {
		addition.TokenType = "Bearer"
	}
	return &client{
		addition: addition,
		http:     &http.Client{Timeout: 5 * time.Minute},
		onSave:   onSave,
	}
}

func (c *client) apiURL(path string) string {
	return fmt.Sprintf(apiEndpoint, c.addition.DomainID) + path
}

func (c *client) authURL(path string) string {
	return fmt.Sprintf(authEndpoint, c.addition.DomainID) + path
}

func (c *client) refreshToken(ctx context.Context) error {
	if c.addition.RefreshToken == "" {
		return nil
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", c.addition.RefreshToken)
	form.Set("client_id", c.addition.ClientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authURL("/v2/oauth/token"), bytes.NewBufferString(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("refresh token failed: %s: %s", resp.Status, string(data))
	}

	var token struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &token); err != nil {
		return err
	}
	if token.AccessToken == "" {
		return fmt.Errorf("refresh token failed: access_token is empty")
	}
	c.addition.AccessToken = token.AccessToken
	if token.TokenType != "" {
		c.addition.TokenType = token.TokenType
	}
	if token.RefreshToken != "" {
		c.addition.RefreshToken = token.RefreshToken
	}
	c.addition.ExpiresAt = 0
	if c.onSave != nil {
		c.onSave()
	}
	return nil
}

func (c *client) ensureToken(ctx context.Context) error {
	if c.addition.RefreshToken == "" {
		return nil
	}
	if c.addition.AccessToken == "" {
		return c.refreshToken(ctx)
	}
	if c.addition.ExpiresAt > 0 && time.Now().Unix() >= c.addition.ExpiresAt-300 {
		return c.refreshToken(ctx)
	}
	return nil
}

func (c *client) post(ctx context.Context, path string, body any, out any) error {
	if err := c.ensureToken(ctx); err != nil {
		return err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	data, statusCode, status, err := c.postPayload(ctx, path, payload)
	if err != nil {
		return err
	}
	if statusCode >= 400 && isAccessTokenExpiredError(statusCode, data) && c.addition.RefreshToken != "" {
		if err := c.refreshToken(ctx); err != nil {
			return err
		}
		data, statusCode, status, err = c.postPayload(ctx, path, payload)
		if err != nil {
			return err
		}
	}
	if statusCode >= 400 {
		return fmt.Errorf("pds api %s failed: %s: %s", path, status, string(data))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *client) postPayload(ctx context.Context, path string, payload []byte) ([]byte, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL(path), bytes.NewReader(payload))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Authorization", c.addition.TokenType+" "+c.addition.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, "", err
	}
	return data, resp.StatusCode, resp.Status, nil
}

func isAccessTokenExpiredError(statusCode int, data []byte) bool {
	if statusCode < http.StatusBadRequest {
		return false
	}
	var apiErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	text := string(data)
	if len(data) > 0 && json.Unmarshal(data, &apiErr) == nil {
		text = apiErr.Code + " " + apiErr.Message + " " + apiErr.Error
	}
	text = strings.ToLower(text)
	for _, marker := range []string{
		"accesstokenexpired",
		"access token expired",
		"accesstokeninvalid",
		"access token invalid",
		"invalidaccesstoken",
		"invalid access token",
		"token expired",
		"expiredtoken",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (c *client) putRaw(ctx context.Context, uploadURL string, r io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, r)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pds upload failed: %s: %s", resp.Status, string(data))
	}
	return nil
}
