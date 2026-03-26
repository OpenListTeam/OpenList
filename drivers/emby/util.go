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
)

var episodeCodeRegexp = regexp.MustCompile(`(?i)\bS\d{1,2}E\d{1,2}\b`)

func (d *Emby) login(ctx context.Context) error {
	payload, err := json.Marshal(authReq{
		Username: d.Username,
		Pw:       d.Password,
	})
	if err != nil {
		return err
	}

	endpoint := d.URL + "/Users/AuthenticateByName"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Client="OpenList", Device="OpenList", DeviceId="openlist-emby", Version="1.0.0"`)

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("emby auth failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var data authResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}
	if strings.TrimSpace(data.AccessToken) == "" || strings.TrimSpace(data.User.ID) == "" {
		return fmt.Errorf("emby auth response missing access token or user id")
	}

	d.token = data.AccessToken
	d.userID = data.User.ID
	return nil
}

func (d *Emby) getItems(ctx context.Context, parentID string) (*listResp, error) {
	u, err := url.Parse(d.URL + "/Users/" + d.userID + "/Items")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("ParentId", parentID)
	q.Set("Recursive", "false")
	q.Set("Fields", "Path,Size,DateCreated,SeriesName,IndexNumber,ParentIndexNumber")
	q.Set("api_key", d.token)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("emby list failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var data listResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}
