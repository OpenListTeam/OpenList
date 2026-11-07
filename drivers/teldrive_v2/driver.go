package teldrive_v2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	hash_extend "github.com/OpenListTeam/OpenList/v4/pkg/utils/hash"
	"github.com/go-resty/resty/v2"
)

// listPageSize is the maximum the v2 API accepts for `limit`.
const listPageSize = 200

// treeHashBlockMiB is teldrive's fixed tree-hash block size (internal/treehash).
const treeHashBlockMiB = 16

// maxChunkMiB is the largest part Telegram will take as a single message.
const maxChunkMiB = 2000

// defaultChunkMiB matches the part size the server picks for itself.
const defaultChunkMiB = 512

// hashAlgorithm is the only value teldrive v2 reports in FileEntry.hash.
const hashAlgorithm = "blake3"

// defaultShareExpireHours is used when the configured value cannot be read.
const defaultShareExpireHours = 1

// shareRenewMargin keeps a share from being handed out right before it lapses.
const shareRenewMargin = 5 * time.Minute

// cachedShare holds the one-time token the API returns when a share is created.
type cachedShare struct {
	token     string
	expiresAt time.Time
}

type TeldriveV2 struct {
	model.Storage
	Addition

	// rootID is the resolved UUID of RootFolderPath; "" means the drive root.
	rootID string
	// pathCache memoises absolute drive path -> folder UUID.
	pathCache *sync.Map
	// shareCache memoises file UUID -> public share token.
	shareCache *sync.Map
	// lastShareSweep is the unix-nano time shareCache was last swept, so the
	// sweep cost is amortised instead of paid on every mint.
	lastShareSweep atomic.Int64
	// uploadClient carries part bodies: no client deadline, no implicit retry.
	uploadClient *resty.Client
	// shareExpire is Addition.ShareExpire parsed into a duration.
	shareExpire time.Duration
}

func (d *TeldriveV2) Config() driver.Config { return config }

func (d *TeldriveV2) GetAddition() driver.Additional { return &d.Addition }

func (d *TeldriveV2) Init(ctx context.Context) error {
	d.Address = strings.TrimSuffix(strings.TrimSpace(d.Address), "/")
	if d.Address == "" {
		return fmt.Errorf("url is required")
	}
	if strings.TrimSpace(d.ApiKey) == "" {
		return fmt.Errorf("api_key is required")
	}
	if d.UploadConcurrency <= 0 {
		d.UploadConcurrency = 4
	}
	if d.UploadConcurrency > 16 {
		d.UploadConcurrency = 16
	}
	d.ChunkSize = normalizeChunkSize(d.ChunkSize)
	d.shareExpire = parseShareExpire(d.ShareExpire)

	d.pathCache = &sync.Map{}
	d.shareCache = &sync.Map{}
	// Inherits the shared proxy/TLS/UA setup, then drops the two settings that
	// are wrong for a multi-hundred-MiB body.
	//
	// The pre-request hook is what keeps a part streaming. resty's own
	// SetContentLength drains an io.Reader body into memory to measure it
	// (middleware.go handleRequestBody), which would buffer a whole part and
	// make progress meaningless - the bytes would all be "sent" long before
	// they reach Telegram. Instead the caller sets a Content-Length header and
	// this copies it onto the raw request, so net/http streams the body with a
	// known length.
	d.uploadClient = base.NewRestyClient().
		SetTimeout(0).
		SetRetryCount(0).
		SetPreRequestHook(func(_ *resty.Client, req *http.Request) error {
			if req.ContentLength > 0 || req.Body == nil {
				return nil
			}
			v := req.Header.Get("Content-Length")
			if v == "" {
				return nil
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid Content-Length %q: %w", v, err)
			}
			req.ContentLength = n
			return nil
		})

	rootID, err := d.resolveFolderID(ctx, d.RootFolderPath)
	if err != nil {
		return err
	}
	d.rootID = rootID

	op.MustSaveDriverStorage(d)
	return nil
}

func (d *TeldriveV2) Drop(ctx context.Context) error {
	d.pathCache = &sync.Map{}
	d.shareCache = &sync.Map{}
	return nil
}

// GetRoot hands OpenList a root object that already carries the resolved UUID,
// so every write operation below can pass a parentId without another lookup.
func (d *TeldriveV2) GetRoot(ctx context.Context) (model.Obj, error) {
	return &model.Object{
		ID:       d.rootID,
		Path:     normalizePath(d.RootFolderPath),
		Name:     "root",
		IsFolder: true,
	}, nil
}

func (d *TeldriveV2) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	parentID, err := d.dirID(ctx, dir)
	if err != nil {
		return nil, err
	}
	dirPath := normalizePath(dir.GetPath())

	// parentID == "" is the drive root, which needs no query parameter at all.
	entries, err := d.listAll(ctx, parentID, "")
	if err != nil {
		return nil, err
	}

	return utils.SliceConvert(entries, func(src FileEntry) (model.Obj, error) {
		return d.toObj(&src, path.Join(dirPath, src.Name)), nil
	})
}

func (d *TeldriveV2) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	// One endpoint serves both streaming and downloading: it answers Range
	// requests with 206, and its Content-Disposition already carries the stored
	// filename either way. The trailing segment is for clients that take the
	// name from the URL instead - players and command-line tools - which would
	// otherwise see the UUID. The segment-less form remains as downloadFileLegacy.
	name := url.PathEscape(file.GetName())
	if d.UseShareLink {
		token, expiresAt, err := d.shareToken(ctx, file.GetID())
		if err != nil {
			return nil, err
		}
		link := &model.Link{
			URL: fmt.Sprintf("%s%s/public/shares/%s/files/%s/content/%s",
				d.Address, apiPrefix, url.PathEscape(token), url.PathEscape(file.GetID()), name),
		}
		// Without this OpenList caches the link against its SyncClosers, which
		// never expire for a plain URL, so it would keep handing out the token
		// long after the share behind it lapsed. Expiring the cache a little
		// early sends the next request back through here for a fresh one.
		if ttl := time.Until(expiresAt) - shareRenewMargin; ttl > 0 {
			link.Expiration = &ttl
		}
		return link, nil
	}
	return &model.Link{
		URL: fmt.Sprintf("%s%s/files/%s/content/%s", d.Address, apiPrefix, url.PathEscape(file.GetID()), name),
		Header: http.Header{
			"X-Api-Key": {d.ApiKey},
		},
	}, nil
}

func (d *TeldriveV2) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	parentID, err := d.dirID(ctx, parentDir)
	if err != nil {
		return nil, err
	}
	body := base.Json{
		"name":           dirName,
		"conflictPolicy": PolicyFail,
	}
	if parentID != "" {
		body["parentId"] = parentID
	}

	newPath := path.Join(normalizePath(parentDir.GetPath()), dirName)

	entry := &FileEntry{}
	if err := d.request(ctx, http.MethodPost, "/folders", idempotent(func(req *resty.Request) {
		req.SetBody(body)
	}), entry); err != nil {
		// conflictPolicy=fail returns 409 when the folder is already there.
		// Treat that as success and hand back the existing folder.
		var e *ErrResp
		if errors.As(err, &e) && e.status == http.StatusConflict {
			id, resolveErr := d.resolveFolderID(ctx, newPath)
			if resolveErr != nil {
				return nil, err
			}
			return &model.Object{ID: id, Path: newPath, Name: dirName, IsFolder: true}, nil
		}
		return nil, err
	}
	return d.toObj(entry, newPath), nil
}

func (d *TeldriveV2) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	dstID, err := d.dirID(ctx, dstDir)
	if err != nil {
		return nil, err
	}
	body := base.Json{"conflictPolicy": PolicyReplace}
	if dstID != "" {
		body["parentId"] = dstID
	}

	entry := &FileEntry{}
	if err := d.request(ctx, http.MethodPost, "/files/{id}/move", idempotent(func(req *resty.Request) {
		req.SetPathParam("id", srcObj.GetID())
		req.SetBody(body)
	}), entry); err != nil {
		return nil, err
	}
	d.forgetPath(srcObj)
	return d.toObj(entry, path.Join(normalizePath(dstDir.GetPath()), entry.Name)), nil
}

func (d *TeldriveV2) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	entry := &FileEntry{}
	if err := d.request(ctx, http.MethodPatch, "/files/{id}", func(req *resty.Request) {
		req.SetPathParam("id", srcObj.GetID())
		req.SetBody(base.Json{"name": newName})
	}, entry); err != nil {
		return nil, err
	}
	d.forgetPath(srcObj)
	parent := path.Dir(normalizePath(srcObj.GetPath()))
	return d.toObj(entry, path.Join(parent, entry.Name)), nil
}

// Copy is a single call in v2: the server copies the whole subtree
// transactionally, so the recursive CopyManager the v1 driver needs is gone.
func (d *TeldriveV2) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	dstID, err := d.dirID(ctx, dstDir)
	if err != nil {
		return nil, err
	}
	body := base.Json{
		"name":           srcObj.GetName(),
		"conflictPolicy": PolicyReplace,
	}
	if dstID != "" {
		body["parentId"] = dstID
	}

	entry := &FileEntry{}
	if err := d.request(ctx, http.MethodPost, "/files/{id}/copy", idempotent(func(req *resty.Request) {
		req.SetPathParam("id", srcObj.GetID())
		req.SetBody(body)
	}), entry); err != nil {
		return nil, err
	}
	return d.toObj(entry, path.Join(normalizePath(dstDir.GetPath()), entry.Name)), nil
}

// Remove moves the object to trash. With HardDelete enabled it is then purged,
// which v2 only permits on an already-trashed object.
func (d *TeldriveV2) Remove(ctx context.Context, obj model.Obj) error {
	if err := d.request(ctx, http.MethodDelete, "/files/{id}", func(req *resty.Request) {
		req.SetPathParam("id", obj.GetID())
	}, nil); err != nil {
		return err
	}
	d.forgetPath(obj)
	d.shareCache.Delete(obj.GetID())
	if !d.HardDelete {
		return nil
	}
	return d.request(ctx, http.MethodDelete, "/files/{id}/purge", func(req *resty.Request) {
		req.SetPathParam("id", obj.GetID())
	}, nil)
}

func (d *TeldriveV2) toObj(entry *FileEntry, objPath string) model.Obj {
	isFolder := entry.Kind == KindFolder
	size := entry.Size
	if isFolder {
		size = 0
	}
	// The server hashes every part as it streams to Telegram, so a file entry
	// always carries its tree hash - it costs us nothing to surface it.
	var hashInfo utils.HashInfo
	if entry.Hash != nil && strings.EqualFold(entry.Hash.Algorithm, hashAlgorithm) {
		hashInfo = utils.NewHashInfo(hash_extend.BLAKE3Tree, entry.Hash.Value)
	}
	return &model.Object{
		ID:       entry.ID,
		Path:     objPath,
		Name:     entry.Name,
		Size:     size,
		IsFolder: isFolder,
		HashInfo: hashInfo,
		// v2 renamed updatedAt -> modTime and round-trips the client mtime,
		// so this is the timestamp the file actually had on upload.
		Modified: entry.ModTime,
		Ctime:    entry.CreatedAt,
	}
}

func (d *TeldriveV2) forgetPath(obj model.Obj) {
	if obj.IsDir() {
		d.pathCache.Delete(normalizePath(obj.GetPath()))
	}
}

// sweepShares drops entries whose share has lapsed.
//
// Every entry expires within shareExpire, so a sweep leaves behind only shares
// that are still usable: the map is bounded by the number of distinct files
// linked within one expiry window rather than by everything ever linked. It
// runs off the mint path - already an HTTP round trip - and no more than once
// per shareRenewMargin, so a busy storage does not pay the walk on every miss.
func (d *TeldriveV2) sweepShares(now time.Time) {
	last := d.lastShareSweep.Load()
	if now.UnixNano()-last < int64(shareRenewMargin) {
		return
	}
	if !d.lastShareSweep.CompareAndSwap(last, now.UnixNano()) {
		// Another goroutine is sweeping; one pass is enough.
		return
	}
	d.shareCache.Range(func(k, v any) bool {
		if s, ok := v.(*cachedShare); ok && !now.Before(s.expiresAt.Add(-shareRenewMargin)) {
			d.shareCache.Delete(k)
		}
		return true
	})
}

// shareToken returns a public token for the file, minting one only when needed.
//
// The token cannot be recovered after the fact: GET /files/{id}/shares lists a
// share's id, expiry and download count but never its token, which the API hands
// back exactly once at creation. So reusing a share means remembering the token
// ourselves - without this cache every Link() call would leave another live
// share behind on the server.
func (d *TeldriveV2) shareToken(ctx context.Context, fileID string) (string, time.Time, error) {
	if v, ok := d.shareCache.Load(fileID); ok {
		if s := v.(*cachedShare); time.Now().Before(s.expiresAt.Add(-shareRenewMargin)) {
			return s.token, s.expiresAt, nil
		}
		d.shareCache.Delete(fileID)
	}

	now := time.Now().UTC()
	d.sweepShares(now)

	expires := now.Add(d.shareExpire)
	share := &ShareCreated{}
	if err := d.request(ctx, http.MethodPost, "/files/{id}/shares", idempotent(func(req *resty.Request) {
		req.SetPathParam("id", fileID)
		req.SetBody(base.Json{
			"permission": "read",
			"expiresAt":  expires.Format(time.RFC3339),
		})
	}), share); err != nil {
		return "", time.Time{}, err
	}
	if share.Token == "" {
		return "", time.Time{}, fmt.Errorf("[TeldriveV2] share created without a token")
	}

	if !share.ExpiresAt.IsZero() {
		expires = share.ExpiresAt
	}
	d.shareCache.Store(fileID, &cachedShare{token: share.Token, expiresAt: expires})
	return share.Token, expires, nil
}

func (d *TeldriveV2) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	return nil, errs.NotImplement
}

func (d *TeldriveV2) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *TeldriveV2) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	return nil, errs.NotImplement
}

func (d *TeldriveV2) ArchiveDecompress(ctx context.Context, srcObj, dstDir model.Obj, args model.ArchiveDecompressArgs) ([]model.Obj, error) {
	return nil, errs.NotImplement
}

var (
	_ driver.Driver       = (*TeldriveV2)(nil)
	_ driver.GetRooter    = (*TeldriveV2)(nil)
	_ driver.MkdirResult  = (*TeldriveV2)(nil)
	_ driver.MoveResult   = (*TeldriveV2)(nil)
	_ driver.RenameResult = (*TeldriveV2)(nil)
	_ driver.CopyResult   = (*TeldriveV2)(nil)
	_ driver.Remove       = (*TeldriveV2)(nil)
	_ driver.PutResult    = (*TeldriveV2)(nil)
)
