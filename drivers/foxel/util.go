package foxel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
)

func (d *Foxel) currentToken() string {
	d.tokenMu.RLock()
	defer d.tokenMu.RUnlock()
	return d.accessToken
}

func (d *Foxel) setToken(token string) {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	d.accessToken = token
}

// Serialize login and reuse a token refreshed by another request.
func (d *Foxel) refreshToken(ctx context.Context, previous string) error {
	d.loginMu.Lock()
	defer d.loginMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if token := d.currentToken(); token != "" && token != previous {
		return nil
	}
	if d.Username == "" || d.Password == "" {
		return fmt.Errorf("Foxel: username and password are required to refresh the access token")
	}
	form := url.Values{"username": {d.Username}, "password": {d.Password}}
	encoded := form.Encode()
	res, err := d.send(ctx, http.MethodPost, "/auth/login", nil,
		strings.NewReader(encoded), "application/x-www-form-urlencoded", int64(len(encoded)), "")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return responseError(res.StatusCode, payload)
	}
	// Foxel's OAuth2 login is the one endpoint without the code/msg/data envelope.
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err = json.Unmarshal(payload, &result); err != nil {
		return fmt.Errorf("Foxel: invalid login response: %w", err)
	}
	if result.AccessToken == "" {
		return fmt.Errorf("Foxel: login returned an empty access token")
	}
	d.setToken(result.AccessToken)
	return nil
}

func (d *Foxel) apiURL(api string, query url.Values) string {
	endpoint := d.Address + "/api" + api
	if len(query) != 0 {
		endpoint += "?" + query.Encode()
	}
	return endpoint
}

func (d *Foxel) send(ctx context.Context, method, api string, query url.Values,
	body io.Reader, contentType string, size int64, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, d.apiURL(api, query), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", base.UserAgent)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if body != nil {
		req.ContentLength = size
	}
	return d.client.Do(req)
}

func (d *Foxel) request(ctx context.Context, method, api string, query url.Values, body, result any) error {
	ctx, cancel := context.WithTimeout(ctx, base.DefaultTimeout)
	defer cancel()
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		token := d.currentToken()
		if token == "" {
			if err = d.refreshToken(ctx, token); err != nil {
				return err
			}
			token = d.currentToken()
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(payload)
		}
		res, err := d.send(ctx, method, api, query, reader, "application/json", int64(len(payload)), token)
		if err != nil {
			return err
		}
		// A 403 is a permission denial, not a reason to login or retry a mutation.
		if res.StatusCode == http.StatusUnauthorized && attempt == 0 && d.Username != "" && d.Password != "" {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
			res.Body.Close()
			if err = d.refreshToken(ctx, token); err != nil {
				return err
			}
			continue
		}
		err = decodeResponse(res, result)
		res.Body.Close()
		return err
	}
	return fmt.Errorf("Foxel: authentication failed after refreshing the access token")
}

func decodeResponse(res *http.Response, result any) error {
	payload, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return responseError(res.StatusCode, payload)
	}
	var envelope response
	if err = json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("Foxel: invalid API response: %w", err)
	}
	if envelope.Code == nil {
		return fmt.Errorf("Foxel: API response is missing its status code")
	}
	if *envelope.Code != 0 {
		return fmt.Errorf("Foxel: API error %d: %s", *envelope.Code, envelope.Msg)
	}
	if result != nil {
		if err = json.Unmarshal(envelope.Data, result); err != nil {
			return fmt.Errorf("Foxel: invalid response data: %w", err)
		}
	}
	return nil
}

func responseError(status int, payload []byte) error {
	var envelope response
	_ = json.Unmarshal(payload, &envelope)
	message := envelope.Msg
	if len(envelope.Detail) != 0 && string(envelope.Detail) != "null" {
		if json.Unmarshal(envelope.Detail, &message) != nil {
			message = string(envelope.Detail)
		}
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return fmt.Errorf("Foxel: HTTP %d: %s", status, message)
}

// Escape each segment rather than escaping slashes or leaving '?' and '#' in filenames.
func fsAPI(operation, fullPath string) string {
	parts := strings.Split(strings.TrimPrefix(fullPath, "/"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	if operation != "" {
		operation += "/"
	}
	return "/fs/" + operation + strings.Join(parts, "/")
}

func childPath(parent, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return "", fmt.Errorf("Foxel: invalid file or directory name %q", name)
	}
	return path.Join(parent, name), nil
}

func (d *Foxel) downloadURL(link tempLink, filename string) (string, error) {
	if link.URL == "" {
		if link.Token == "" {
			return "", fmt.Errorf("Foxel: temporary link response is missing its URL and token")
		}
		return d.apiURL("/fs/download-public/"+url.PathEscape(link.Token)+"/"+url.PathEscape(filename), nil), nil
	}
	u, err := url.Parse(link.URL)
	if err != nil {
		return "", fmt.Errorf("Foxel: invalid temporary link URL: %w", err)
	}
	if !u.IsAbs() {
		baseURL, _ := url.Parse(d.Address + "/")
		if u.Host == "" {
			// Foxel returns /api/... even when mounted under a reverse-proxy prefix.
			u, err = url.Parse(strings.TrimPrefix(link.URL, "/"))
			if err != nil {
				return "", err
			}
		}
		u = baseURL.ResolveReference(u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("Foxel: temporary link must be an HTTP(S) URL")
	}
	const publicPath = "/api/fs/public/"
	if !strings.Contains(u.Path, publicPath) {
		return "", fmt.Errorf("Foxel: unexpected temporary link URL path")
	}
	// The preview endpoint converts RAW images; OpenList must download the original bytes.
	u.Path = strings.Replace(u.Path, publicPath, "/api/fs/download-public/", 1)
	if u.RawPath != "" {
		u.RawPath = strings.Replace(u.RawPath, publicPath, "/api/fs/download-public/", 1)
	}
	return u.String(), nil
}

func (d *Foxel) waitTask(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("Foxel: queued transfer is missing its task ID")
	}
	for {
		var task taskStatus
		if err := d.request(ctx, http.MethodGet, "/tasks/queue/"+url.PathEscape(id), nil, nil, &task); err != nil {
			return err
		}
		switch task.Status {
		case "success":
			return nil
		case "failed":
			return fmt.Errorf("Foxel: transfer task %s failed: %s", id, task.Error)
		case "pending", "running":
		default:
			return fmt.Errorf("Foxel: transfer task %s has unexpected status %q", id, task.Status)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
