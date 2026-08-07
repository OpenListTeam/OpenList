package emby

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

var episodeCodeRegexp = regexp.MustCompile(`(?i)\bS\d{1,2}E\d{1,2}\b`)

const embyPageSize = 1000

type embyHTTPError struct {
	action     string
	statusCode int
	body       string
}

func (e *embyHTTPError) Error() string {
	return fmt.Sprintf("emby %s failed: status=%d body=%s", e.action, e.statusCode, e.body)
}

func (e *embyHTTPError) unauthorized() bool {
	return e.statusCode == http.StatusUnauthorized
}

func (d *Emby) login(ctx context.Context) error {
	d.authMu.Lock()
	defer d.authMu.Unlock()

	token, userID, err := d.authenticate(ctx)
	if err != nil {
		return err
	}
	d.token = token
	d.userID = userID
	return nil
}

func (d *Emby) authenticate(ctx context.Context) (string, string, error) {
	payload, err := json.Marshal(authReq{
		Username: d.Username,
		Pw:       d.Password,
	})
	if err != nil {
		return "", "", err
	}

	endpoint := d.URL + "/Users/AuthenticateByName"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Client="OpenList", Device="OpenList", DeviceId="openlist-emby", Version="1.0.0"`)

	resp, err := d.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("emby auth failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var data authResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(data.AccessToken) == "" || strings.TrimSpace(data.User.ID) == "" {
		return "", "", fmt.Errorf("emby auth response missing access token or user id")
	}

	return strings.TrimSpace(data.AccessToken), strings.TrimSpace(data.User.ID), nil
}

func (d *Emby) getItems(ctx context.Context, parentID string) ([]embyItem, error) {
	_, userID := d.auth()
	items := make([]embyItem, 0)
	for startIndex := 0; ; {
		var page listResp
		query := url.Values{}
		query.Set("ParentId", parentID)
		query.Set("Recursive", "false")
		query.Set("Fields", "Path,Size,DateCreated,SeriesName,IndexNumber,ParentIndexNumber")
		query.Set("StartIndex", fmt.Sprintf("%d", startIndex))
		query.Set("Limit", fmt.Sprintf("%d", embyPageSize))
		if err := d.getJSON(ctx, "/Users/"+userID+"/Items", query, &page, "list"); err != nil {
			return nil, err
		}

		if len(page.Items) == 0 && startIndex < page.TotalRecordCount {
			return nil, fmt.Errorf("emby list returned an empty page at start index %d before total count %d", startIndex, page.TotalRecordCount)
		}
		items = append(items, page.Items...)
		startIndex += len(page.Items)
		if startIndex >= page.TotalRecordCount || len(page.Items) == 0 {
			return items, nil
		}
	}
}

func (d *Emby) getViews(ctx context.Context) ([]embyItem, error) {
	_, userID := d.auth()
	var data listResp
	if err := d.getJSON(ctx, "/Users/"+userID+"/Views", nil, &data, "views"); err != nil {
		return nil, err
	}
	return data.Items, nil
}

func (d *Emby) getItemDetail(ctx context.Context, fileID string) (*itemDetailResp, error) {
	_, userID := d.auth()
	var detail itemDetailResp
	query := url.Values{}
	query.Set("Fields", "MediaSources,MediaType")
	if err := d.getJSON(ctx, "/Users/"+userID+"/Items/"+fileID, query, &detail, "item detail"); err != nil {
		return nil, err
	}
	return &detail, nil
}

func (d *Emby) getJSON(ctx context.Context, endpoint string, query url.Values, out any, action string) error {
	token, _ := d.auth()
	err := d.doGetJSON(ctx, endpoint, query, token, out, action)
	if err == nil {
		return nil
	}
	requestErr, ok := err.(*embyHTTPError)
	if !ok || !requestErr.unauthorized() || strings.TrimSpace(d.Username) == "" || strings.TrimSpace(d.Password) == "" {
		return err
	}

	if err := d.relogin(ctx, token); err != nil {
		return err
	}
	newToken, _ := d.auth()
	return d.doGetJSON(ctx, endpoint, query, newToken, out, action)
}

func (d *Emby) doGetJSON(ctx context.Context, endpoint string, query url.Values, token string, out any, action string) error {
	u, err := url.Parse(d.URL + endpoint)
	if err != nil {
		return err
	}
	q := u.Query()
	for key, values := range query {
		q.Del(key)
		for _, value := range values {
			q.Add(key, value)
		}
	}
	q.Set("api_key", token)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return &embyHTTPError{action: action, statusCode: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (d *Emby) relogin(ctx context.Context, staleToken string) error {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	if d.token != staleToken {
		return nil
	}

	token, userID, err := d.authenticate(ctx)
	if err != nil {
		return err
	}
	d.token = token
	d.userID = userID
	d.ApiKey = token
	d.UserID = userID
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *Emby) auth() (string, string) {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	return d.token, d.userID
}

func (d *Emby) setAuth(token, userID string) {
	d.authMu.Lock()
	d.token = token
	d.userID = userID
	d.authMu.Unlock()
}

func (d *Emby) saveAuth() {
	token, userID := d.auth()
	d.ApiKey = token
	d.UserID = userID
}
