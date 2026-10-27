package ctfile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL   = "https://rest.ctfile.com/v1"
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"
	rootFolderID     = "d0"
	maxListPages     = 500
)

// sessionExpiredMsg is the human-readable error for 401 responses.
const sessionExpiredMsg = "[CtFile] Session expired (401). Please regenerate your session token in the CtFile backend under 「开放接口登录密钥管理」"

// stripIDPrefix removes a leading d or f prefix from an ID when the next
// character is a digit. E.g. "d123" → "123", "f456" → "456", "abc" → "abc".
var stripPrefixRe = regexp.MustCompile(`^([df])(\d)`)

func stripIDPrefix(id string) string {
	if m := stripPrefixRe.FindStringSubmatch(id); m != nil {
		return id[1:] // drop the leading letter
	}
	return id
}

func (d *CtFile) ns() string {
	if d.CloudType == "private" {
		return "private"
	}
	return "public"
}

func (d *CtFile) baseURL() string {
	if u := strings.TrimRight(d.BaseURL, "/"); u != "" {
		return u
	}
	return defaultBaseURL
}

func (d *CtFile) ua() string {
	if d.UserAgent != "" {
		return d.UserAgent
	}
	return defaultUserAgent
}

// orderby maps the user fields to CtFile's server-side sort tokens.
func (d *CtFile) orderby() string {
	keys := map[string]string{"name": "az", "size": "big", "time": "new"}
	rev := map[string]string{"az": "za", "big": "small", "new": "old"}
	base, ok := keys[d.OrderBy]
	if !ok {
		base = "az"
	}
	if d.OrderDirection == "desc" {
		return rev[base]
	}
	return base
}

// post is the raw transport layer: POST JSON, include session, retry on
// 429/5xx, map 401 to a clear error. Returns decoded JSON map or error.
func (d *CtFile) post(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	session := strings.TrimSpace(d.Session)
	if session == "" {
		return nil, fmt.Errorf("[CtFile] session is not configured; please fill in the session token from 开放接口登录密钥管理")
	}

	payload := make(map[string]any, len(body)+1)
	for k, v := range body {
		payload[k] = v
	}
	payload["session"] = session

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("[CtFile] json marshal: %w", err)
	}

	url := d.baseURL() + path
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(300*attempt) * time.Millisecond):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", d.ua())

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		// Retry on 429 / 5xx
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}

		body2, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}

		var result map[string]any
		if err := json.Unmarshal(body2, &result); err != nil {
			return nil, fmt.Errorf("[CtFile] %s: non-JSON response (HTTP %d): %.200s", path, resp.StatusCode, body2)
		}

		code := int(toInt64(result["code"]))
		if code == 401 {
			return nil, fmt.Errorf("%s", sessionExpiredMsg)
		}
		if code == 429 {
			lastErr = fmt.Errorf("[CtFile] rate limited (code 429)")
			continue
		}
		return result, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("[CtFile] %s failed after retries: %w", path, lastErr)
	}
	return nil, fmt.Errorf("[CtFile] %s failed after retries", path)
}

// call wraps post and checks code == 200.
func (d *CtFile) call(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	result, err := d.post(ctx, path, body)
	if err != nil {
		return nil, err
	}
	// Missing / zero code is treated as success (Worker: code === undefined → ok).
	if _, hasCode := result["code"]; hasCode {
		code := int(toInt64(result["code"]))
		if code != 200 {
			return result, &apiError{code: code, message: toString(result["message"]), raw: result}
		}
	}
	return result, nil
}

// callWithId calls an endpoint that takes a single id parameter, implementing
// the dual-try strategy (prefer one form; on 400/404 retry the other).
//
// prefer = "raw"      → try id as-is first, fallback to stripped
// prefer = "stripped" → try stripped first, fallback to raw
func (d *CtFile) callWithId(ctx context.Context, path, idKey, id string, extra map[string]any, prefer string) (map[string]any, error) {
	stripped := stripIDPrefix(id)
	var primary, secondary string
	if prefer == "stripped" {
		primary, secondary = stripped, id
	} else {
		primary, secondary = id, stripped
	}

	body := make(map[string]any, len(extra)+1)
	for k, v := range extra {
		body[k] = v
	}

	body[idKey] = primary
	result, err := d.call(ctx, path, body)
	if err != nil {
		var ae *apiError
		if asAPIError(err, &ae) && (ae.code == 400 || ae.code == 404) && primary != secondary {
			body[idKey] = secondary
			return d.call(ctx, path, body)
		}
		return nil, err
	}
	return result, nil
}

// callWithIds calls an endpoint that takes a list of ids; tries comma-string
// first, on 400/404/405 retries JSON array.
func (d *CtFile) callWithIds(ctx context.Context, path string, ids []string, extra map[string]any) (map[string]any, error) {
	body := make(map[string]any, len(extra)+2)
	for k, v := range extra {
		body[k] = v
	}

	// First attempt: comma-separated string
	body["ids"] = strings.Join(ids, ",")
	result, err := d.call(ctx, path, body)
	if err != nil {
		var ae *apiError
		if asAPIError(err, &ae) && (ae.code == 400 || ae.code == 404 || ae.code == 405) {
			// Second attempt: JSON array
			body["ids"] = ids
			return d.call(ctx, path, body)
		}
		return nil, err
	}
	return result, nil
}

// --------------------------------------------------------------------------
// High-level API helpers
// --------------------------------------------------------------------------

// profile fetches the user's profile; used to validate the session in Init.
func (d *CtFile) profile(ctx context.Context) error {
	_, err := d.call(ctx, "/user/info/profile", nil)
	return err
}

// listFolders returns all sub-folders for a given parent folder ID.
func (d *CtFile) listFolders(ctx context.Context, folderID string) ([]folderEntry, error) {
	if folderID == "" || folderID == "0" {
		folderID = rootFolderID
	}
	result, err := d.call(ctx, "/"+d.ns()+"/folder/list", map[string]any{
		"folder_id": folderID,
		"orderby":   d.orderby(),
	})
	if err != nil {
		return nil, err
	}
	items := toSlice(result["results"])
	out := make([]folderEntry, 0, len(items))
	for _, raw := range items {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, folderEntry{
			Key:  toString(m["key"]),
			Name: toString(m["name"]),
			Icon: toString(m["icon"]),
			Date: m["date"],
		})
	}
	return out, nil
}

// listFiles returns all files in a folder, paginating until empty or duplicate.
func (d *CtFile) listFiles(ctx context.Context, folderID string) ([]fileEntry, error) {
	if folderID == "" || folderID == "0" {
		folderID = rootFolderID
	}
	var all []fileEntry
	start := 0
	prevFirstKey := ""

	for page := 0; page < maxListPages; page++ {
		result, err := d.call(ctx, "/"+d.ns()+"/file/list", map[string]any{
			"folder_id": folderID,
			"start":     start,
			"orderby":   d.orderby(),
		})
		if err != nil {
			return nil, err
		}
		items := toSlice(result["results"])
		if len(items) == 0 {
			break
		}

		// Guard against server ignoring `start` and looping forever
		firstKey := ""
		if m, ok := items[0].(map[string]any); ok {
			firstKey = toString(m["key"])
		}
		if page > 0 && firstKey == prevFirstKey {
			break
		}
		prevFirstKey = firstKey

		for _, raw := range items {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			all = append(all, fileEntry{
				Key:    toString(m["key"]),
				Name:   toString(m["name"]),
				Icon:   toString(m["icon"]),
				ImgSrc: toString(m["imgsrc"]),
				Size:   toInt64(m["size"]),
				Date:   m["date"],
				Status: int(toInt64(m["status"])),
			})
		}
		start += len(items)
	}
	return all, nil
}

// mkdir creates a folder and returns its ID. 403 with an existing folder_id is
// treated as success (folder already exists).
func (d *CtFile) mkdir(ctx context.Context, parentID, name string) (string, error) {
	if parentID == "" || parentID == "0" {
		parentID = rootFolderID
	}
	result, err := d.call(ctx, "/"+d.ns()+"/folder/create", map[string]any{
		"folder_id":   parentID,
		"name":        name,
		"description": "",
		"is_hidden":   false,
	})
	if err != nil {
		var ae *apiError
		if asAPIError(err, &ae) && ae.code == 403 {
			// Folder already exists; the response still carries folder_id
			if id := toString(ae.raw["folder_id"]); id != "" {
				return id, nil
			}
		}
		return "", err
	}
	id := toString(result["folder_id"])
	if id == "" {
		id = toString(result["path"])
	}
	return id, nil
}

// fetchURL returns the CDN download URL for a file.
func (d *CtFile) fetchURL(ctx context.Context, fileID string) (string, error) {
	result, err := d.callWithId(ctx, "/"+d.ns()+"/file/fetch_url", "file_id", fileID, nil, "stripped")
	if err != nil {
		return "", err
	}
	url := toString(result["download_url"])
	if url == "" {
		return "", fmt.Errorf("[CtFile] fetch_url returned empty download_url")
	}
	return url, nil
}

// renameFile renames a file.
func (d *CtFile) renameFile(ctx context.Context, fileID, newName string) error {
	_, err := d.callWithId(ctx, "/"+d.ns()+"/file/modify_meta", "file_id", fileID, map[string]any{"name": newName}, "stripped")
	return err
}

// renameFolder renames a folder.
func (d *CtFile) renameFolder(ctx context.Context, folderID, newName string) error {
	_, err := d.callWithId(ctx, "/"+d.ns()+"/folder/modify_meta", "folder_id", folderID, map[string]any{
		"name":      newName,
		"is_rename": true,
	}, "raw")
	return err
}

// remove moves files/folders to trash.
func (d *CtFile) remove(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := d.callWithIds(ctx, "/"+d.ns()+"/file/delete", ids, nil)
	return err
}

// move moves files/folders to another folder.
func (d *CtFile) move(ctx context.Context, ids []string, targetFolderID string) error {
	if len(ids) == 0 {
		return nil
	}
	if targetFolderID == "" || targetFolderID == "0" {
		targetFolderID = rootFolderID
	}
	_, err := d.callWithIds(ctx, "/"+d.ns()+"/file/move", ids, map[string]any{"folder_id": targetFolderID})
	return err
}

// copyFiles copies files to a target folder.
func (d *CtFile) copyFiles(ctx context.Context, ids []string, targetFolderID string) error {
	if len(ids) == 0 {
		return nil
	}
	if targetFolderID == "" || targetFolderID == "0" {
		targetFolderID = rootFolderID
	}
	_, err := d.callWithIds(ctx, "/"+d.ns()+"/file/copy", ids, map[string]any{"folder_id": targetFolderID})
	return err
}

// uploadFile uploads a stream to CtFile using the two-step ticket+multipart flow.
// checksum format: md5hex + "-" + size
func (d *CtFile) uploadFile(ctx context.Context, folderID, fileName string, content io.Reader, size int64, md5hex string) error {
	if folderID == "" || folderID == "0" {
		folderID = rootFolderID
	}

	checksum := fmt.Sprintf("%s-%d", md5hex, size)

	// Step 1: get upload ticket
	result, err := d.call(ctx, "/"+d.ns()+"/file/upload", map[string]any{
		"folder_id": folderID,
		"checksum":  checksum,
		"size":      size,
		"name":      fileName,
	})
	if err != nil {
		return err
	}

	exists := toInt64(result["exists"])
	if exists > 0 {
		// Instant upload / deduplication
		return nil
	}

	uploadURL := toString(result["upload_url"])
	if uploadURL == "" {
		return fmt.Errorf("[CtFile] upload ticket returned no upload_url")
	}

	// Step 2: multipart upload to the presigned URL
	return d.multipartUpload(ctx, uploadURL, fileName, content, size)
}

// countingWriter counts bytes without retaining the written data.
type countingWriter int64

func (w *countingWriter) Write(p []byte) (int, error) {
	*w += countingWriter(len(p))
	return len(p), nil
}

func writeMultipartFields(mw *multipart.Writer, fileName string, size int64) (io.Writer, error) {
	if err := mw.WriteField("name", fileName); err != nil {
		return nil, err
	}
	if err := mw.WriteField("filesize", strconv.FormatInt(size, 10)); err != nil {
		return nil, err
	}
	return mw.CreateFormFile("file", fileName)
}

func multipartLayout(fileName string, size int64) (int64, string, error) {
	var count countingWriter
	mw := multipart.NewWriter(&count)
	if _, err := writeMultipartFields(mw, fileName, size); err != nil {
		return 0, "", err
	}
	if err := mw.Close(); err != nil {
		return 0, "", err
	}
	return int64(count) + size, mw.Boundary(), nil
}

// multipartUpload POSTs name+filesize+file to a presigned upload URL without
// retaining the complete multipart body in memory.
func (d *CtFile) multipartUpload(ctx context.Context, uploadURL, fileName string, content io.Reader, size int64) error {
	contentLength, boundary, err := multipartLayout(fileName, size)
	if err != nil {
		return err
	}

	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, pr)
	if err != nil {
		return err
	}
	req.ContentLength = contentLength
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("User-Agent", d.ua())

	go func() {
		mw := multipart.NewWriter(pw)
		if err := mw.SetBoundary(boundary); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		fw, err := writeMultipartFields(mw, fileName, size)
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(fw, content); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := mw.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("[CtFile] multipart upload HTTP %d: %.200s", resp.StatusCode, body)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("[CtFile] multipart upload non-JSON: %.200s", body)
	}
	code := int(toInt64(result["code"]))
	if code != 200 {
		return fmt.Errorf("[CtFile] multipart upload failed (code=%d): %s", code, toString(result["message"]))
	}
	return nil
}

// --------------------------------------------------------------------------
// apiError carries a structured error with code + raw body for retry checks.
// --------------------------------------------------------------------------

type apiError struct {
	code    int
	message string
	raw     map[string]any
}

func (e *apiError) Error() string {
	return fmt.Sprintf("[CtFile] API error (code=%d): %s", e.code, e.message)
}

// asAPIError sets dst and returns true when err is an *apiError.
func asAPIError(err error, dst **apiError) bool {
	if ae, ok := err.(*apiError); ok {
		*dst = ae
		return true
	}
	return false
}

// --------------------------------------------------------------------------
// Type-coercion helpers for loosely-typed JSON payloads.
// --------------------------------------------------------------------------

func toInt64(v any) int64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	default:
		return 0
	}
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	default:
		return fmt.Sprintf("%v", v)
	}
}

func toSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}
