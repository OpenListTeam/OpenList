package rakuten_drive

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

const (
	forestBase = "https://forest.sendy.jp/cloud/service/file"
	authBase   = "https://api.rakuten-drive.com/api/v1"
)

func (d *RakutenDrive) ensureAccessToken() error {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	if d.accessToken != "" && !d.accessTokenExpire.IsZero() && time.Now().Before(d.accessTokenExpire.Add(-1*time.Minute)) {
		return nil
	}
	return d.refreshToken()
}

func (d *RakutenDrive) refreshToken() error {
	if d.RefreshToken == "" {
		return fmt.Errorf("refresh_token is required")
	}
	var resp refreshResp
	_, err := base.RestyClient.R().
		SetHeader("content-type", "application/json").
		SetBody(base.Json{"refresh_token": d.RefreshToken}).
		SetResult(&resp).
		Post(authBase + "/auth/refreshtoken")
	if err != nil {
		return err
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
		d.accessTokenExpire = time.Time{}
	}
	return nil
}

func (d *RakutenDrive) newForestRequest(ctx context.Context, method, url string, body interface{}, result interface{}) (*resty.Response, error) {
	if err := d.ensureAccessToken(); err != nil {
		return nil, err
	}
	req := d.client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+d.accessToken).
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

func parseJWTExp(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("invalid jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	exp := utils.Json.Get(payload, "exp").ToInt64()
	if exp == 0 {
		return time.Time{}, fmt.Errorf("exp not found")
	}
	return time.Unix(exp, 0), nil
}

func (d *RakutenDrive) toRemotePath(p string, isDir bool) string {
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

func (d *RakutenDrive) toLocalPath(remote string) string {
	remote = strings.TrimPrefix(remote, "/")
	root := strings.Trim(d.GetRootPath(), "/")
	if root != "" && strings.HasPrefix(remote, root) {
		remote = strings.TrimPrefix(remote, root)
		remote = strings.TrimPrefix(remote, "/")
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
