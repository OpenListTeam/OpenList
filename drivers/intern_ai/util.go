package intern_ai

import (
        "context"
        "fmt"
        "net/url"
        "path"
        "strconv"
        "strings"
        "time"

        "github.com/OpenListTeam/OpenList/v4/drivers/base"
        "github.com/go-resty/resty/v2"
        "github.com/pkg/errors"
)

const (
        defaultAPIBase = "https://discovery.intern-ai.org.cn"
        listEndpoint   = "/api/ml/v1/cloud-disk/list"
        quotaEndpoint  = "/api/ml/v1/cloud-disk/quota"
        mkdirEndpoint  = "/api/ml/v1/cloud-disk/mkdir"
        renameEndpoint = "/api/ml/v1/cloud-disk/rename"
        moveEndpoint   = "/api/ml/v1/cloud-disk/move"
        copyEndpoint   = "/api/ml/v1/cloud-disk/copy"
        deleteEndpoint = "/api/ml/v1/cloud-disk/delete"
        downloadURL    = "/api/ml/v1/cloud-disk/download-url"
        uploadURLPath  = "/api/ml/v1/cloud-disk/upload-url"
)

// cleanKey normalizes a drive path into the file_key / dir_path form used by
// the cloud-disk API: absolute, no trailing slash (except the root itself).
func cleanKey(p string) string {
        p = strings.ReplaceAll(p, "\\", "/")
        if !strings.HasPrefix(p, "/") {
                p = "/" + p
        }
        p = path.Clean(p)
        return p
}

// joinKey joins a directory key and a name.
func joinKey(dir, name string) string {
        return cleanKey(path.Join(cleanKey(dir), name))
}

func isErrResp(code int) bool { return code != 0 }

// request performs an authenticated cloud-disk API call and decodes the envelope.
func (d *InternAI) request(ctx context.Context, ep string, method string, query url.Values, body any) (*apiResp, error) {
        var resp apiResp
        req := base.RestyClient.R().
                SetContext(ctx).
                SetHeader("Authorization", "Bearer "+d.Token).
                SetHeader("Accept", "application/json").
                SetResult(&resp)
        if query != nil {
                req = req.SetQueryParamsFromValues(query)
        }
        if body != nil {
                req = req.SetBody(body)
        }
        r, err := req.Execute(method, d.apiBase+ep)
        if err != nil {
                return nil, errors.Wrapf(err, "intern_ai request %s failed", ep)
        }
        if r.IsError() {
                if r.StatusCode() == 401 {
                        return nil, errors.New("intern_ai: invalid or expired token (401)")
                }
                return nil, fmt.Errorf("intern_ai request %s returned HTTP %d: %s", ep, r.StatusCode(), r.String()[:min(200, len(r.String()))])
        }
        if isErrResp(resp.Code) {
                return nil, fmt.Errorf("intern_ai api %s error: code=%d msg=%s", ep, resp.Code, resp.Msg)
        }
        // Normalize the envelope-less shape some endpoints answer with: the
        // production upload-url endpoint returns {"urls":[...]} at the top
        // level instead of {"code":0,"data":{"urls":[...]}}.
        if len(resp.Data.URLs) == 0 && len(resp.TopLevelURLs) > 0 {
                resp.Data.URLs = resp.TopLevelURLs
        }
        return &resp, nil
}

func (d *InternAI) get(ctx context.Context, ep string, query url.Values) (*apiResp, error) {
        return d.request(ctx, ep, resty.MethodGet, query, nil)
}

func (d *InternAI) post(ctx context.Context, ep string, body any) (*apiResp, error) {
        return d.request(ctx, ep, resty.MethodPost, nil, body)
}

// listPage fetches one page of dir listing.
func (d *InternAI) listPage(ctx context.Context, dirPath, pageToken string, dirsOnly bool) ([]fileItem, string, error) {
        if d.PageSize <= 0 {
                d.PageSize = 100
        }
        q := url.Values{}
        q.Set("page_size", fmt.Sprintf("%d", d.PageSize))
        q.Set("page_token", pageToken)
        q.Set("dir_path", cleanKey(dirPath))
        q.Set("keyword", "")
        q.Set("sort_by", "name")
        q.Set("sort_order", "asc")
        if dirsOnly {
                q.Set("dirs_only", "true")
        }
        resp, err := d.get(ctx, listEndpoint, q)
        if err != nil {
                return nil, "", err
        }
        return resp.Data.Files, resp.Data.NextToken, nil
}

// listAll iterates through all pages of a directory.
func (d *InternAI) listAll(ctx context.Context, dirPath string, dirsOnly bool) ([]fileItem, error) {
        var out []fileItem
        pageToken := ""
        for i := 0; ; i++ {
                if i >= 1000 { // safety valve
                        return nil, errors.New("intern_ai: too many listing pages")
                }
                files, next, err := d.listPage(ctx, dirPath, pageToken, dirsOnly)
                if err != nil {
                        return nil, err
                }
                out = append(out, files...)
                if next == "" || next == pageToken {
                        break
                }
                pageToken = next
        }
        return out, nil
}

// getDownloadLink resolves the presigned download URL of one file.
func (d *InternAI) getDownloadLink(ctx context.Context, key string) (string, error) {
        resp, err := d.post(ctx, downloadURL, base.Json{"file_keys": []string{cleanKey(key)}})
        if err != nil {
                return "", err
        }
        for _, u := range resp.Data.URLs {
                if u.FileKey != "" && cleanKey(u.FileKey) != cleanKey(key) {
                        continue
                }
                if u.Skipped || u.DownloadURL == "" {
                        continue
                }
                return u.DownloadURL, nil
        }
        return "", errors.Errorf("no download url for %s (folder or unsupported type)", key)
}

// timeLayouts covers the timestamp shapes seen on the platform.
var timeLayouts = []string{
        time.RFC3339Nano,
        time.RFC3339,
        "2006-01-02T15:04:05.999Z0700",
        "2006-01-02 15:04:05",
        "2006-01-02",
}

func parseTime(s string) time.Time {
        s = strings.TrimSpace(s)
        if s == "" {
                return time.Time{}
        }
        for _, layout := range timeLayouts {
                if t, err := time.Parse(layout, s); err == nil {
                        return t
                }
        }
        if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
                if ms > 1e12 { // milliseconds
                        return time.UnixMilli(ms)
                }
                return time.Unix(ms, 0)
        }
        return time.Time{}
}

func fileExt(name string) string {
        n := path.Base(name)
        if i := strings.LastIndex(n, "."); i >= 0 {
                return strings.ToLower(n[i+1:])
        }
        return ""
}
