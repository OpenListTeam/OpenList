package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

const apiBase = "https://open.feishu.cn/open-apis"

type apiError struct{ Status, Code int }

func (e *apiError) Error() string {
	hint := "check Feishu application scopes and resource permissions"
	if e.Status == 401 || e.Code == 99991663 || e.Code == 99991668 {
		hint = "authorization expired; reauthorize the storage"
	}
	return fmt.Sprintf("Feishu API rejected request (HTTP %d, code %d); %s", e.Status, e.Code, hint)
}

// Responses and transport errors are deliberately not embedded in errors: they
// can contain application secrets, authorization codes or private document data.
func readJSON(resp *http.Response, dst any) error {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return fmt.Errorf("invalid or oversized Feishu API response")
	}
	var envelope struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil {
		return fmt.Errorf("invalid Feishu API response (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || *envelope.Code != 0 {
		return &apiError{Status: resp.StatusCode, Code: *envelope.Code}
	}
	if dst != nil && json.Unmarshal(envelope.Data, dst) != nil {
		return fmt.Errorf("invalid Feishu API response data")
	}
	return nil
}

func (s *session) request(ctx context.Context, method, endpoint string, body io.Reader, contentType string, authenticated bool, length ...int64) (*http.Response, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("cannot construct Feishu request")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if len(length) != 0 {
		req.ContentLength = length[0]
	}
	if authenticated {
		token, err := s.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Feishu API connection failed or timed out")
	}
	return resp, nil
}

func (s *session) json(ctx context.Context, method, endpoint string, body, dst any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("cannot encode Feishu request")
		}
	}
	// Only retry reads. Mutations (including refresh token exchange) may have
	// succeeded even when their response was lost.
	for attempt := 0; ; attempt++ {
		resp, err := s.request(ctx, method, endpoint, bytes.NewReader(data), "application/json; charset=utf-8", true)
		if err != nil {
			return err
		}
		if method == http.MethodGet && attempt < 2 && (resp.StatusCode == 429 || resp.StatusCode >= 500) {
			delay := time.Duration(1<<attempt) * time.Second
			if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 && seconds <= 10 {
				delay = time.Duration(seconds) * time.Second
			}
			resp.Body.Close()
			if err := wait(ctx, delay); err != nil {
				return err
			}
			continue
		}
		return readJSON(resp, dst)
	}
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func newLimiter() *rate.Limiter { return rate.NewLimiter(rate.Every(250*time.Millisecond), 1) }
