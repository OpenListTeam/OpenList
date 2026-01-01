package _123_open

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

var (
	AccessToken  = "https://open-api.123pan.com/api/v1/access_token"
	RefreshToken = "https://open-api.123pan.com/api/v1/oauth2/access_token"
)

type tokenManager struct {
	// accessToken  string
	expiredAt    time.Time
	mu           sync.Mutex
	blockRefresh bool
}

func (d *Open123) getAccessToken(forceRefresh bool) (string, error) {
	tm := d.tm
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.blockRefresh {
		return "", errors.New("Authentication expired")
	}
	if !forceRefresh && d.AccessToken != "" && time.Now().Before(tm.expiredAt.Add(-5*time.Minute)) {
		return d.AccessToken, nil
	}
	if err := d.flushAccessToken(); err != nil {
		// token expired and failed to refresh, block further refresh attempts
		tm.blockRefresh = true
		return "", err
	}
	return d.AccessToken, nil
}

func (d *Open123) flushAccessToken() error {
	// directly send request to avoid deadlock
	req := base.RestyClient.R()
	req.SetHeaders(map[string]string{
		"authorization": "Bearer " + d.AccessToken,
		"platform":      "open_platform",
		"Content-Type":  "application/json",
	})

	if d.ClientID != "" {
		if d.RefreshToken != "" {
			var resp RefreshTokenResp
			req.SetQueryParam("client_id", d.ClientID)
			if d.ClientSecret != "" {
				req.SetQueryParam("client_secret", d.ClientSecret)
			}
			req.SetQueryParam("grant_type", "refresh_token")
			req.SetQueryParam("refresh_token", d.RefreshToken)
			req.SetResult(&resp)
			res, err := req.Execute(http.MethodPost, RefreshToken)
			if err != nil {
				return err
			}
			body := res.Body()
			var baseResp BaseResp
			if err = json.Unmarshal(body, &baseResp); err != nil {
				return err
			}
			if baseResp.Code != 0 {
				return fmt.Errorf("get access token failed: %s", baseResp.Message)
			}

			d.AccessToken = resp.AccessToken
			// add token expire time
			d.tm.expiredAt = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
			d.RefreshToken = resp.RefreshToken
			op.MustSaveDriverStorage(d)
			d.tm.blockRefresh = false
			return nil
		} else if d.ClientSecret != "" {
			var resp AccessTokenResp
			req.SetBody(base.Json{
				"clientID":     d.ClientID,
				"clientSecret": d.ClientSecret,
			})
			req.SetResult(&resp)
			res, err := req.Execute(http.MethodPost, AccessToken)
			if err != nil {
				return err
			}
			body := res.Body()
			var baseResp BaseResp
			if err = json.Unmarshal(body, &baseResp); err != nil {
				return err
			}
			if baseResp.Code != 0 {
				return fmt.Errorf("get access token failed: %s", baseResp.Message)
			}
			d.AccessToken = resp.Data.AccessToken
			// parse token expire time
			d.tm.expiredAt, err = time.Parse(time.RFC3339, resp.Data.ExpiredAt)
			if err != nil {
				return fmt.Errorf("parse expire time failed: %w", err)
			}
			op.MustSaveDriverStorage(d)
			d.tm.blockRefresh = false
			return nil
		}
	}
	return errors.New("no valid authentication method available")
}
