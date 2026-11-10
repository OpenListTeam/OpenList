package feishu

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

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type userToken struct {
	Code         *int   `json:"code"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func validCredential(value string) bool { return value != "" && !strings.ContainsAny(value, "\r\n") }

// Called with tokenMu held. Save only token fields, with a compare-and-swap,
// rather than overwriting concurrent changes to the storage or its settings.
func (s *session) persistToken(ctx context.Context, previous string) error {
	if s.persist != nil {
		return s.persist(s.cfg)
	}
	stored, err := db.GetStorageById(s.storageID)
	if err != nil {
		return fmt.Errorf("cannot load Feishu storage credentials")
	}
	var cfg Addition
	if json.Unmarshal([]byte(stored.Addition), &cfg) != nil || stored.Driver != "Feishu" || stored.Disabled ||
		cfg.AuthMode != "user" || cfg.AppID != s.cfg.AppID || cfg.AppSecret != s.cfg.AppSecret ||
		cfg.RedirectURI != s.cfg.RedirectURI {
		return fmt.Errorf("Feishu storage configuration changed; reload before authorizing")
	}
	if cfg.RefreshToken == s.cfg.RefreshToken && cfg.AccessToken == s.cfg.AccessToken && cfg.TokenExpiresAt == s.cfg.TokenExpiresAt {
		return nil // A prior save succeeded but its acknowledgement was lost.
	}
	if cfg.RefreshToken != previous {
		return fmt.Errorf("Feishu storage credentials changed; reload before authorizing")
	}
	root, err := normalizeRootForMode(cfg.RootFolderID, cfg.AuthMode)
	if err != nil || root != s.cfg.RootFolderID || cfg.ReadOnly != s.cfg.ReadOnly {
		return fmt.Errorf("Feishu storage configuration changed; reload before authorizing")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(stored.Addition), &fields) != nil {
		return fmt.Errorf("invalid saved Feishu configuration")
	}
	fields["access_token"], _ = json.Marshal(s.cfg.AccessToken)
	fields["refresh_token"], _ = json.Marshal(s.cfg.RefreshToken)
	fields["token_expires_at"], _ = json.Marshal(s.cfg.TokenExpiresAt)
	encoded, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("cannot encode Feishu storage credentials")
	}
	// SQL errors must not print the token-bearing addition or CAS predicate.
	result := db.GetDb().Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx).Model(&model.Storage{}).
		Where("id = ? AND addition = ? AND disabled = ?", s.storageID, stored.Addition, false).
		Update("addition", string(encoded))
	if result.Error != nil || result.RowsAffected != 1 {
		return fmt.Errorf("cannot persist Feishu credentials; retry before restarting")
	}
	return nil
}

func (s *session) installToken(ctx context.Context, token userToken) error {
	if !validCredential(token.AccessToken) || !validCredential(token.RefreshToken) || token.ExpiresIn <= 60 || token.ExpiresIn > 365*24*3600 {
		return fmt.Errorf("Feishu did not return renewable user credentials; enable offline_access and reauthorize")
	}
	previous := s.cfg.RefreshToken
	s.cfg.AccessToken, s.cfg.RefreshToken = token.AccessToken, token.RefreshToken
	s.cfg.TokenExpiresAt = time.Now().Unix() + token.ExpiresIn
	s.pendingPrevious = &previous
	if err := s.persistToken(ctx, previous); err != nil {
		return err
	}
	s.pendingPrevious = nil
	return nil
}

func (s *session) exchange(ctx context.Context, body map[string]string) (userToken, error) {
	body["client_id"], body["client_secret"] = s.cfg.AppID, s.cfg.AppSecret
	encoded, _ := json.Marshal(body)
	resp, err := s.request(ctx, http.MethodPost, "/authen/v2/oauth/token", bytes.NewReader(encoded), "application/json", false)
	if err != nil {
		return userToken{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return userToken{}, fmt.Errorf("invalid Feishu token response")
	}
	var token userToken
	if json.Unmarshal(raw, &token) != nil || token.Code == nil {
		return userToken{}, fmt.Errorf("invalid Feishu token response")
	}
	if resp.StatusCode != 200 || *token.Code != 0 {
		return userToken{}, fmt.Errorf("Feishu user authorization failed (HTTP %d, code %d); reauthorize and check offline_access", resp.StatusCode, *token.Code)
	}
	return token, nil
}

func (s *session) accessToken(ctx context.Context) (string, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if s.pendingPrevious != nil {
		if err := s.persistToken(ctx, *s.pendingPrevious); err != nil {
			return "", err
		}
		s.pendingPrevious = nil
	}
	if s.cfg.AuthMode == "user" {
		if validCredential(s.cfg.AccessToken) && time.Now().Unix() < s.cfg.TokenExpiresAt-60 {
			return s.cfg.AccessToken, nil
		}
		if !validCredential(s.cfg.RefreshToken) {
			return "", fmt.Errorf("Feishu user authorization required; authorize this storage in the administration page")
		}
		token, err := s.exchange(ctx, map[string]string{"grant_type": "refresh_token", "refresh_token": s.cfg.RefreshToken})
		if err != nil {
			return "", err
		}
		if err := s.installToken(ctx, token); err != nil {
			return "", err
		}
		return s.cfg.AccessToken, nil
	}
	if s.tenantToken != "" && time.Now().Before(s.tenantExpiry) {
		return s.tenantToken, nil
	}
	encoded, _ := json.Marshal(map[string]string{"app_id": s.cfg.AppID, "app_secret": s.cfg.AppSecret})
	resp, err := s.request(ctx, http.MethodPost, "/auth/v3/tenant_access_token/internal", bytes.NewReader(encoded), "application/json", false)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	var token struct {
		Code   *int   `json:"code"`
		Token  string `json:"tenant_access_token"`
		Expire int64  `json:"expire"`
	}
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &token) != nil || token.Code == nil || resp.StatusCode != 200 || *token.Code != 0 || !validCredential(token.Token) || token.Expire <= 60 || token.Expire > 365*24*3600 {
		return "", fmt.Errorf("Feishu application authorization failed; check App ID and App Secret")
	}
	s.tenantToken, s.tenantExpiry = token.Token, time.Now().Add(time.Duration(token.Expire-60)*time.Second)
	return s.tenantToken, nil
}

// OAuthConfig returns a generation identifier so a callback cannot authorize a
// different configuration after the administrator edits/reloads the storage.
func (d *Feishu) OAuthConfig() (Addition, string, error) {
	s, err := d.current()
	if err != nil {
		return Addition{}, "", err
	}
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.cfg.AuthMode != "user" {
		return Addition{}, "", fmt.Errorf("Feishu storage must use user authentication")
	}
	return s.cfg, s.generation, nil
}

func (d *Feishu) CompleteAuthorization(ctx context.Context, generation, code string) error {
	s, err := d.current()
	if err != nil {
		return err
	}
	if s.generation != generation || !validCredential(code) {
		return fmt.Errorf("Feishu authorization expired; start again")
	}
	ctx, cancel := s.operationContext(ctx, 30*time.Second)
	defer cancel()
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	token, err := s.exchange(ctx, map[string]string{"grant_type": "authorization_code", "code": code, "redirect_uri": s.cfg.RedirectURI})
	if err != nil {
		return err
	}
	return s.installToken(ctx, token)
}

func ValidateRedirectURI(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return nil, fmt.Errorf("Feishu redirect_uri must be an HTTPS callback URL (HTTP is allowed only on loopback)")
	}
	return u, nil
}
