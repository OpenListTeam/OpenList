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
	AccessToken = "https://open-api.123pan.com/api/v1/access_token"
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func resolveExpiredAt(expiredAt string, expiresIn int64) (time.Time, error) {
	if expiredAt != "" {
		t, err := time.Parse(time.RFC3339, expiredAt)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse expire time failed: %w", err)
		}
		return t.UTC(), nil
	}
	if expiresIn > 0 {
		return time.Now().UTC().Add(time.Duration(expiresIn) * time.Second), nil
	}
	return time.Time{}, errors.New("missing expiredAt and expires_in")
}

func readTokenPayload(resp RefreshTokenResp) (accessToken, refreshToken string, expiresIn int64, expiredAt string) {
	accessToken = firstNonEmpty(resp.AccessToken, resp.AccessTokenCamel, resp.Data.AccessToken, resp.Data.AccessTokenCamel)
	refreshToken = firstNonEmpty(resp.RefreshToken, resp.RefreshTokenCamel, resp.Data.RefreshToken, resp.Data.RefreshTokenCamel)
	expiresIn = firstPositive(resp.ExpiresIn, resp.ExpiresInCamel, resp.Data.ExpiresIn, resp.Data.ExpiresInCamel)
	expiredAt = firstNonEmpty(resp.ExpiredAt, resp.Data.ExpiredAt)
	return
}

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
	// 使用在线API刷新Token，无需ClientID和ClientSecret
	if d.UseOnlineAPI && d.RefreshToken != "" && len(d.APIAddress) > 0 {
		u := d.APIAddress
		var resp RefreshTokenResp
		res, err := base.RestyClient.R().
			SetResult(&resp).
			SetQueryParams(map[string]string{
				"refresh_ui": d.RefreshToken,
				"server_use": "true",
				"driver_txt": "123cloud_oa",
			}).
			Get(u)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(res.Body(), &resp); err != nil {
			return err
		}

		accessToken, refreshToken, expiresIn, expiredAtText := readTokenPayload(resp)
		if accessToken == "" || refreshToken == "" {
			errMessage := firstNonEmpty(resp.ErrorDescription, resp.Text, resp.Message, resp.Error)
			if errMessage != "" {
				return fmt.Errorf("failed to refresh token: %s", errMessage)
			}
			return fmt.Errorf("empty access_token or refresh_token returned from official API")
		}
		expiredAt, err := resolveExpiredAt(expiredAtText, expiresIn)
		if err != nil {
			return err
		}

		d.AccessToken = accessToken
		d.RefreshToken = refreshToken
		d.tm.expiredAt = expiredAt
		op.MustSaveDriverStorage(d)
		d.tm.blockRefresh = false
		return nil
	}
	// 走本地开发者API刷新逻辑，必须使用ClientID和ClientSecret
	if d.ClientID != "" && d.ClientSecret != "" {
		req := base.RestyClient.R()
		req.SetHeaders(map[string]string{
			"platform":     "open_platform",
			"Content-Type": "application/json",
		})
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
		expiredAt, err := time.Parse(time.RFC3339, resp.Data.ExpiredAt)
		if err != nil {
			return fmt.Errorf("parse expire time failed: %w", err)
		}
		d.AccessToken = resp.Data.AccessToken
		d.tm.expiredAt = expiredAt.UTC()
		op.MustSaveDriverStorage(d)
		d.tm.blockRefresh = false
		return nil
	}
	return errors.New("no valid authentication method available")
}
