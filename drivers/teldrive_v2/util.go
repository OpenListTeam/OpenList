package teldrive_v2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
)

const apiPrefix = "/api/v1"

// request performs an authenticated metadata call against the v2 API.
// Unlike v1 (cookie `access_token`), v2 authenticates with the X-Api-Key header.
func (d *TeldriveV2) request(ctx context.Context, method, pathname string, callback base.ReqCallback, resp any) error {
	return d.do(base.RestyClient, ctx, method, pathname, callback, resp)
}

// uploadRequest sends a part over the driver's own client.
//
// base.RestyClient caps every request at base.DefaultTimeout (30s), which a part
// upload blows straight through: the server only answers once it has relayed the
// whole part to Telegram. The shared client also retries three times on its own,
// which would replay a part body behind the back of our retry policy.
func (d *TeldriveV2) uploadRequest(ctx context.Context, method, pathname string, callback base.ReqCallback, resp any) error {
	return d.do(d.uploadClient, ctx, method, pathname, callback, resp)
}

func (d *TeldriveV2) do(client *resty.Client, ctx context.Context, method, pathname string, callback base.ReqCallback, resp any) error {
	req := client.R().SetContext(ctx)
	req.SetHeader("X-Api-Key", d.ApiKey)
	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}
	e := &ErrResp{}
	req.SetError(e)

	res, err := req.Execute(method, d.Address+apiPrefix+pathname)
	if err != nil {
		return err
	}
	if res.IsError() {
		e.status = res.StatusCode()
		if e.Detail.Message == "" {
			e.Detail.Message = res.Status()
		}
		return e
	}
	return nil
}

// idempotent stamps the Idempotency-Key header that v2 requires on every
// mutating endpoint (folders, move, copy, shares, uploads, uploads/complete).
// One fresh UUID per logical operation: the key only has to stay stable across
// retries of that same call, which resty replays with the header intact.
func idempotent(callback base.ReqCallback) base.ReqCallback {
	key := uuid.NewString()
	return func(req *resty.Request) {
		req.SetHeader("Idempotency-Key", key)
		if callback != nil {
			callback(req)
		}
	}
}

// retryable reports whether an error is worth another attempt.
// Anything the server answered with a 4xx is a decision, not a hiccup: a 409 on
// a part whose size or checksum disagrees, a 410 on an expired session and a
// 422 on a malformed body all reproduce exactly on replay. Only overload (429),
// server faults (5xx) and transport errors get retried.
func retryable(err error) bool {
	var e *ErrResp
	if !errors.As(err, &e) {
		// Transport-level failure with no HTTP response - worth retrying.
		return true
	}
	return e.status == http.StatusTooManyRequests || e.status >= http.StatusInternalServerError
}

// normalizeChunkSize brings a configured part size, in MiB, into the range the
// server will not enforce for us.
//
// The server does not sanity-check preferredPartSize at all: it echoes back
// whatever is asked for, 1 MiB or 10 GiB alike. So both bounds are load-bearing.
// The lower one exists because the file hash is BLAKE3 over the concatenated
// 16 MiB block hashes of every part, and a part that is not a whole number of
// blocks shifts the block boundaries for every later part. The upper one exists
// because a part becomes a single Telegram message.
func normalizeChunkSize(mib int64) int64 {
	if mib <= 0 {
		return defaultChunkMiB
	}
	if rem := mib % treeHashBlockMiB; rem != 0 {
		mib += treeHashBlockMiB - rem
	}
	if mib > maxChunkMiB {
		return maxChunkMiB
	}
	return mib
}

// parseShareExpire turns the configured hour count into a duration, falling
// back to the default for anything unusable so a bad value cannot take the
// storage down.
func parseShareExpire(v string) time.Duration {
	hours, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || hours <= 0 {
		hours = defaultShareExpireHours
	}
	return time.Duration(hours) * time.Hour
}

// listPage fetches a single cursor page. Exactly one of parentID / dirPath is
// used: v2 rejects requests that carry both.
func (d *TeldriveV2) listPage(ctx context.Context, parentID, dirPath, cursor string, extra map[string]string) (*ListResp, error) {
	resp := &ListResp{}
	err := d.request(ctx, http.MethodGet, "/files", func(req *resty.Request) {
		params := map[string]string{
			"limit":  strconv.Itoa(listPageSize),
			"status": "active",
		}
		if parentID != "" {
			params["parentId"] = parentID
		} else if dirPath != "" {
			params["path"] = dirPath
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		for k, v := range extra {
			params[k] = v
		}
		req.SetQueryParams(params)
	}, resp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// listAll walks every cursor page of a folder.
// v1 could fan out pages in parallel because it knew meta.totalPages up front;
// v2 is cursor-based, so pages must be fetched sequentially.
func (d *TeldriveV2) listAll(ctx context.Context, parentID, dirPath string) ([]FileEntry, error) {
	var (
		items  []FileEntry
		cursor string
	)
	for {
		resp, err := d.listPage(ctx, parentID, dirPath, cursor, nil)
		if err != nil {
			return nil, err
		}
		items = append(items, resp.Items...)
		if resp.NextCursor == "" {
			return items, nil
		}
		cursor = resp.NextCursor
	}
}

// normalizePath turns any input into a clean absolute drive path ("" -> "/").
func normalizePath(p string) string {
	p = path.Clean("/" + strings.TrimSpace(p))
	if p == "." {
		return "/"
	}
	return p
}

// resolveFolderID maps an absolute drive path to a folder UUID.
// An empty string means the drive root: v2 treats a missing parentId as root,
// so callers pass it straight through and simply omit the field.
func (d *TeldriveV2) resolveFolderID(ctx context.Context, folderPath string) (string, error) {
	folderPath = normalizePath(folderPath)
	if folderPath == "/" {
		return "", nil
	}
	if id, ok := d.pathCache.Load(folderPath); ok {
		return id.(string), nil
	}

	parent, name := path.Split(folderPath)
	resp, err := d.listPage(ctx, "", normalizePath(parent), "", map[string]string{
		"search": name,
		"kind":   KindFolder,
	})
	if err != nil {
		return "", err
	}
	for _, it := range resp.Items {
		if it.Kind == KindFolder && it.Name == name {
			d.pathCache.Store(folderPath, it.ID)
			return it.ID, nil
		}
	}
	return "", fmt.Errorf("[TeldriveV2] folder not found: %s", folderPath)
}

// dirID returns the folder UUID for a listed object, falling back to a path
// lookup for objects that reached us without an ID.
func (d *TeldriveV2) dirID(ctx context.Context, dir model.Obj) (string, error) {
	if id := dir.GetID(); id != "" {
		return id, nil
	}
	return d.resolveFolderID(ctx, dir.GetPath())
}
