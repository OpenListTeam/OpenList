package modelscope

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	"github.com/pkg/errors"
)

const (
	legacyAPIPrefix = "/api/v1"
)

// repoSegment returns the plural URL segment for a repo type ("model" ->
// "models", "dataset" -> "datasets").
func (d *ModelScope) repoSegment() string {
	t := d.RepoType
	if t == "" {
		t = "dataset"
	}
	if strings.HasSuffix(t, "s") {
		return t
	}
	return t + "s"
}

// endpoint returns the configured endpoint with a trailing slash removed.
func (d *ModelScope) endpoint() string {
	return strings.TrimRight(d.Endpoint, "/")
}

// buildURL constructs a full legacy-API URL from a repo-relative path.
func (d *ModelScope) buildURL(path string) string {
	return d.endpoint() + legacyAPIPrefix + "/" + strings.TrimLeft(path, "/")
}

// apiURL constructs a URL under /api/v1/{type}s/{repoID}/...
func (d *ModelScope) apiURL(suffix string) string {
	return d.buildURL(d.repoSegment() + "/" + d.RepoID + "/" + suffix)
}

// authHeaders returns the headers used for authenticated requests. ModelScope
// accepts a Bearer token and, for older endpoints, an m_session_id cookie. We
// set both to maximise compatibility (mirrors modelscope_hub _headers).
func (d *ModelScope) authHeaders() map[string]string {
	h := map[string]string{
		"Content-Type": "application/json",
	}
	if t := strings.TrimSpace(d.Token); t != "" {
		h["Authorization"] = "Bearer " + t
		h["Cookie"] = "m_session_id=" + t
	}
	return h
}

// checkErr inspects a resty response and returns a descriptive error on non-2xx.
func checkErr(res *resty.Response) error {
	if res.StatusCode() >= 200 && res.StatusCode() < 300 {
		return nil
	}
	var e errResp
	if err := jsonUnmarshal(res.Body(), &e); err == nil && e.text() != "" {
		return errors.Errorf("modelscope %s: %s", res.Status(), e.text())
	}
	return errors.Errorf("modelscope %s: %s", res.Status(), string(res.Body()))
}

// jsonUnmarshal is a small indirection so utils.Json is the single source of
// JSON handling (consistent with the rest of the drivers).
func jsonUnmarshal(data []byte, v any) error {
	return utils.Json.Unmarshal(data, v)
}

// getFileList fetches the (possibly recursive) file tree of the repository at
// the configured revision, following dataset pagination when present.
func (d *ModelScope) getFileList(ctx context.Context, root string, recursive bool) ([]FileEntry, error) {
	var all []FileEntry

	if d.RepoType == "dataset" {
		// Datasets paginate via the repo/tree endpoint (TotalCount/PageNumber).
		page := 1
		pageSize := 200
		for {
			entries, total, err := d.getFileListPage(ctx, root, recursive, page, pageSize)
			if err != nil {
				return nil, err
			}
			all = append(all, entries...)
			if total > 0 && len(all) >= total {
				break
			}
			if len(entries) < pageSize {
				break
			}
			page++
		}
		return all, nil
	}

	// Models/other: repo/files does not paginate; it silently truncates at
	// REPO_FILES_TRUNCATION_LIMIT. We fetch the single page.
	entries, _, err := d.getFileListPage(ctx, root, recursive, 0, 0)
	return entries, err
}

// getFileListPage performs one listing request and returns the entries plus the
// total count (0 if the endpoint does not report it).
func (d *ModelScope) getFileListPage(ctx context.Context, root string, recursive bool, page, pageSize int) ([]FileEntry, int, error) {
	params := map[string]string{
		"Revision":  d.Revision,
		"Recursive": fmt.Sprintf("%t", recursive),
	}
	if root != "" && root != "/" {
		params["Root"] = root
	}

	var suffix string
	if d.RepoType == "dataset" {
		suffix = "repo/tree"
		params["PageNumber"] = fmt.Sprintf("%d", page)
		params["PageSize"] = fmt.Sprintf("%d", pageSize)
	} else {
		suffix = "repo/files"
	}

	res, err := base.NewRestyClient().R().
		SetHeaders(d.authHeaders()).
		SetQueryParams(params).
		SetContext(ctx).
		Get(d.apiURL(suffix))
	if err != nil {
		return nil, 0, err
	}
	if err := checkErr(res); err != nil {
		return nil, 0, err
	}

	// Try the full envelope (with Data wrapper + outer pagination fields).
	var env fileListEnvelope
	if err := jsonUnmarshal(res.Body(), &env); err == nil && env.Data.Files != nil {
		total := env.TotalCount
		if total == 0 {
			total = env.Data.TotalCount
		}
		return env.Data.Files, total, nil
	}

	// A bare array is also possible.
	var raw rawFileList
	if err := jsonUnmarshal(res.Body(), &raw); err == nil {
		return []FileEntry(raw), 0, nil
	}

	// Unwrapped file list (no Data wrapper).
	var resp fileListResp
	if err := jsonUnmarshal(res.Body(), &resp); err != nil {
		return nil, 0, err
	}
	return resp.Files, resp.TotalCount, nil
}

// downloadURL builds the raw download URL for a file.
//
// Pattern: {endpoint}/api/v1/{type}s/{repo_id}/repo?Revision={rev}&FilePath={path}
func (d *ModelScope) downloadURL(filePath string) string {
	q := url.Values{}
	q.Set("Revision", d.Revision)
	q.Set("FilePath", filePath)
	return d.apiURL("repo") + "?" + q.Encode()
}

// downloadHeaders returns the headers needed when fetching a file. For public
// repos no auth is needed, but the token is harmless and required for private
// repos.
func (d *ModelScope) downloadHeaders() http.Header {
	h := http.Header{}
	if t := strings.TrimSpace(d.Token); t != "" {
		h.Set("Authorization", "Bearer "+t)
		h.Set("Cookie", "m_session_id="+t)
	}
	return h
}
