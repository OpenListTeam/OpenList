package rakuten_drive

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

func newHTTPClient() *resty.Client {
	return base.NewRestyClient().SetHeaders(map[string]string{
		"Accept":       "application/json, text/plain, */*",
		"content-type": "application/json",
	})
}

const (
	forestBase = "https://forest.sendy.jp/cloud/service/file"
	authBase   = "https://api.rakuten-drive.com/api/v1"

	// s3MaxUploadParts is the maximum number of parts allowed in one S3 multipart upload
	s3MaxUploadParts = 10000
)

func (d *RakutenDrive) ensureAccessToken() error {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	if d.accessToken != "" && !d.accessTokenExpire.IsZero() && time.Now().Before(d.accessTokenExpire.Add(-1*time.Minute)) {
		return nil
	}
	return d.doRefreshAccessToken()
}

// doRefreshAccessToken exchanges the stored refresh token for a new id token
func (d *RakutenDrive) doRefreshAccessToken() error {
	if d.RefreshToken == "" {
		return fmt.Errorf("refresh_token is required")
	}
	if d.client == nil {
		d.client = newHTTPClient()
	}
	var resp refreshResp
	res, err := d.client.R().
		SetHeader("content-type", "application/json").
		SetBody(base.Json{"refresh_token": d.RefreshToken}).
		SetResult(&resp).
		Post(authBase + "/auth/refreshtoken")
	if err != nil {
		return err
	}
	if res.IsError() {
		return fmt.Errorf("refresh token failed with status %d: %s", res.StatusCode(), strings.TrimSpace(res.String()))
	}
	if resp.IDToken == "" {
		return fmt.Errorf("refresh token failed: idToken empty")
	}
	if resp.RefreshToken != "" {
		d.RefreshToken = resp.RefreshToken
		op.MustSaveDriverStorage(d)
	}
	d.accessToken = resp.IDToken
	if exp, err := parseJWTExp(resp.IDToken); err == nil {
		d.accessTokenExpire = exp
	} else {
		// fall back to a conservative validity so we don't refresh on every request
		d.accessTokenExpire = time.Now().Add(30 * time.Minute)
	}
	return nil
}

func (d *RakutenDrive) newForestRequest(ctx context.Context, method, url string, body interface{}, result interface{}) (*resty.Response, error) {
	if err := d.ensureAccessToken(); err != nil {
		return nil, err
	}
	resp, err := d.doForestRequest(ctx, method, url, body, result)
	if err != nil {
		return resp, err
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		// the cached access token may have been revoked; force one refresh and retry
		d.invalidateAccessToken()
		if err := d.ensureAccessToken(); err != nil {
			return resp, err
		}
		resp, err = d.doForestRequest(ctx, method, url, body, result)
		if err != nil {
			return resp, err
		}
	}
	if resp.IsError() {
		return resp, fmt.Errorf("request %s %s failed with status %d: %s", method, url, resp.StatusCode(), strings.TrimSpace(resp.String()))
	}
	return resp, nil
}

func (d *RakutenDrive) invalidateAccessToken() {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	d.accessToken = ""
}

func (d *RakutenDrive) doForestRequest(ctx context.Context, method, url string, body interface{}, result interface{}) (*resty.Response, error) {
	req := d.client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+d.accessToken).
		// the file API only accepts requests that appear to come from the web app
		SetHeader("Origin", "https://www.rakuten-drive.com").
		SetHeader("Referer", "https://www.rakuten-drive.com/")
	if d.UploadToken != "" {
		req.SetHeader("token", d.UploadToken)
	}
	if body != nil {
		req.SetBody(body)
	}
	if result != nil {
		req.SetResult(result)
	}
	return req.Execute(method, url)
}

func (d *RakutenDrive) getSTSCredentials(ctx context.Context, remoteDir string) (*filelinkTokenResp, error) {
	d.stsMu.Lock()
	defer d.stsMu.Unlock()
	if d.stsCreds != nil && d.stsCredsDir == remoteDir && time.Now().Before(d.stsCredsExpire) {
		return d.stsCreds, nil
	}
	tokenURL := forestBase + "/v1/filelink/token?host_id=" + url.QueryEscape(d.HostID) + "&path=" + url.QueryEscape(remoteDir)
	var tokenResp filelinkTokenResp
	_, err := d.newForestRequest(ctx, http.MethodGet, tokenURL, nil, &tokenResp)
	if err != nil {
		return nil, err
	}
	if tokenResp.AccessKeyID == "" || tokenResp.SecretAccessKey == "" || tokenResp.SessionToken == "" {
		return nil, fmt.Errorf("filelink/token missing credentials")
	}
	expire := parseTimeAny(tokenResp.Expiration)
	if expire.IsZero() {
		// unknown expiration; use a conservative TTL
		expire = time.Now().Add(30 * time.Minute)
	} else if !time.Now().Before(expire.Add(-time.Minute)) {
		// credentials are already within the safety margin; use them
		// for this call but do not cache them past their real expiry
		return &tokenResp, nil
	}
	d.stsCreds = &tokenResp
	d.stsCredsDir = remoteDir
	// drop the cache one minute before the credential actually expires
	d.stsCredsExpire = expire.Add(-time.Minute)
	return d.stsCreds, nil
}

func parseJWTExp(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("invalid jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, err
	}
	exp := utils.Json.Get(payload, "exp").ToInt64()
	if exp == 0 {
		return time.Time{}, fmt.Errorf("exp not found")
	}
	return time.Unix(exp, 0), nil
}

func (d *RakutenDrive) localPath2RemotePath(p string, isDir bool) string {
	p = strings.TrimPrefix(p, "/")
	root := strings.Trim(d.GetRootPath(), "/")
	if root != "" {
		if p != "" {
			p = path.Join(root, p)
		} else {
			p = root
		}
	}
	if isDir {
		if p != "" && !strings.HasSuffix(p, "/") {
			p += "/"
		}
		return p
	}
	return strings.TrimSuffix(p, "/")
}

func (d *RakutenDrive) remotePath2LocalPath(remote string) string {
	remote = strings.TrimPrefix(remote, "/")
	root := strings.Trim(d.GetRootPath(), "/")
	if root != "" && strings.HasPrefix(remote, root) {
		// only strip on a path-segment boundary so sibling names that merely
		// start with the root folder are not mangled
		if rest := remote[len(root):]; rest == "" || strings.HasPrefix(rest, "/") {
			remote = strings.TrimPrefix(rest, "/")
		}
	}
	if remote == "" {
		return "/"
	}
	return "/" + remote
}

func normalizeFilePath(baseDir, itemPath string) string {
	if itemPath == "" {
		return strings.TrimSuffix(baseDir, "/")
	}
	if strings.Contains(itemPath, "/") {
		return strings.TrimPrefix(itemPath, "/")
	}
	baseDir = strings.TrimPrefix(baseDir, "/")
	if baseDir == "" {
		return itemPath
	}
	return path.Join(strings.TrimSuffix(baseDir, "/"), itemPath)
}

func parseTimeAny(v interface{}) time.Time {
	switch t := v.(type) {
	case string:
		if t == "" {
			return time.Time{}
		}
		if ts, err := time.Parse(time.RFC3339, t); err == nil {
			return ts
		}
		if unix, err := time.Parse("2006-01-02 15:04:05", t); err == nil {
			return unix
		}
	case float64:
		return parseUnixFloat(t)
	case int64:
		return parseUnixInt(t)
	case int:
		return parseUnixInt(int64(t))
	}
	return time.Time{}
}

func parseUnixFloat(v float64) time.Time {
	if v > 1e12 {
		return time.UnixMilli(int64(v))
	}
	if v > 1e9 {
		return time.Unix(int64(v), 0)
	}
	return time.Time{}
}

func parseUnixInt(v int64) time.Time {
	if v > 1e12 {
		return time.UnixMilli(v)
	}
	if v > 1e9 {
		return time.Unix(v, 0)
	}
	return time.Time{}
}
