package teldrive_v2

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	hash_extend "github.com/OpenListTeam/OpenList/v4/pkg/utils/hash"
	"github.com/avast/retry-go"
	"github.com/go-resty/resty/v2"
	"golang.org/x/sync/errgroup"
)

const uploadMaxRetries = 3

// Put uploads through the v2 durable upload session:
//
//	POST /uploads                  -> server allocates the id and the part size
//	PUT  /uploads/{id}/parts/{n}   -> idempotent on (uploadId, partNo)
//	POST /uploads/{id}/complete    -> transactional publish
//
// This replaces the v1 dance of minting a client-side uuid, naming every chunk,
// re-reading the remote part list and rebuilding the file from parts+salts.
func (d *TeldriveV2) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	parentID, err := d.dirID(ctx, dstDir)
	if err != nil {
		return nil, err
	}

	body := base.Json{
		"name": file.GetName(),
		"size": file.GetSize(),
		// v2 stores the client mtime, so uploads no longer lose it.
		"modTime": file.ModTime().UTC().Format(time.RFC3339Nano),
		// The server resolves the overwrite itself; no pre-delete round trip.
		"conflictPolicy":    PolicyReplace,
		"preferredPartSize": d.ChunkSize * 1024 * 1024,
	}
	if parentID != "" {
		body["parentId"] = parentID
	}
	if mime := file.GetMimetype(); mime != "" {
		body["mimeType"] = mime
	}

	session, resumed, err := d.getOrCreateSession(ctx, parentID, file, body)
	if err != nil {
		return nil, err
	}

	// A failed session is deliberately left behind so the next attempt can pick
	// up where this one stopped; teldrive expires abandoned sessions itself.
	var stored map[int]UploadPart
	if resumed {
		stored = d.storedParts(ctx, session.ID)
	}

	if err := d.uploadParts(ctx, session, file, up, stored); err != nil {
		return nil, err
	}

	entry := &FileEntry{}
	if err := d.request(ctx, http.MethodPost, "/uploads/{id}/complete", idempotent(func(req *resty.Request) {
		req.SetPathParam("id", session.ID)
	}), entry); err != nil {
		return nil, err
	}

	return d.toObj(entry, utils.FixAndCleanPath(dstDir.GetPath()+"/"+entry.Name)), nil
}

func (d *TeldriveV2) uploadParts(ctx context.Context, session *UploadSession, file model.FileStreamer, up driver.UpdateProgress, stored map[int]UploadPart) error {
	totalSize := file.GetSize()
	if totalSize <= 0 {
		// Empty file: complete publishes it with no parts.
		up(100)
		return nil
	}

	// The part size is the one the server allocated, not the configured
	// preference - v2 aligns it to its own block size.
	partSize := session.PartSize
	if partSize <= 0 {
		partSize = d.ChunkSize * 1024 * 1024
	}
	totalParts := int(math.Ceil(float64(totalSize) / float64(partSize)))

	ss, err := stream.NewStreamSectionReader(file, int(min(partSize, totalSize)), &up)
	if err != nil {
		return err
	}

	progress := &uploadProgress{total: totalSize, up: up}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(int(d.UploadConcurrency))

	for partNo := 1; partNo <= totalParts; partNo++ {
		if utils.IsCanceled(gctx) {
			break
		}
		offset := int64(partNo-1) * partSize
		curSize := min(totalSize-offset, partSize)
		done, resumable := stored[partNo]
		resumable = resumable && done.PlainSize == curSize

		// Without a checksum to compare there is nothing to verify, so trust the
		// recorded size and skip the bytes without ever buffering them.
		if resumable && !d.HashEnabled {
			if err := ss.DiscardSection(offset, curSize); err != nil {
				return err
			}
			progress.commit(curSize)
			continue
		}

		reader, err := ss.GetSectionReader(offset, curSize)
		if err != nil {
			return err
		}

		// Hashing here rather than inside the worker: the same value answers
		// "may this part be skipped" and, if not, becomes its X-Part-Checksum.
		var checksum string
		if d.HashEnabled {
			if _, err := reader.Seek(0, io.SeekStart); err != nil {
				ss.FreeSectionReader(reader)
				return err
			}
			if checksum, err = utils.HashReader(hash_extend.BLAKE3Tree, reader); err != nil {
				ss.FreeSectionReader(reader)
				return err
			}
			if resumable && strings.EqualFold(checksum, done.Checksum) {
				ss.FreeSectionReader(reader)
				progress.commit(curSize)
				continue
			}
		}

		partNo, curSize, reader, checksum := partNo, curSize, reader, checksum
		g.Go(func() error {
			defer ss.FreeSectionReader(reader)
			if err := d.putPart(gctx, session.ID, partNo, curSize, reader, checksum); err != nil {
				return fmt.Errorf("upload part %d: %w", partNo, err)
			}
			progress.commit(curSize)
			return nil
		})
	}

	return g.Wait()
}

// uploadProgress tracks how much of the file the server has actually committed.
//
// A part is credited when its PUT returns, not as its bytes leave us. teldrive
// drains a whole part body at local speed and only then relays it to Telegram -
// measured at 256 MiB absorbed in ~0.5s against a 26s request - so counting
// bytes on the way out would report ~100% while nearly all the work remained.
// Part granularity is the finest signal the API actually gives us; smaller
// chunk_size is what buys a smoother bar.
type uploadProgress struct {
	total int64
	done  atomic.Int64
	up    driver.UpdateProgress
}

func (p *uploadProgress) commit(n int64) {
	p.up(float64(p.done.Add(n)) / float64(p.total) * 100)
}

// putPart uploads one part. PUT is idempotent on (uploadId, partNo), so a retry
// after a mid-flight failure either replays cleanly or returns the stored part.
func (d *TeldriveV2) putPart(ctx context.Context, uploadID string, partNo int, size int64, reader io.ReadSeeker, checksum string) error {
	return retry.Do(func() error {
		if _, err := reader.Seek(0, io.SeekStart); err != nil {
			return err
		}
		part := &UploadPart{}
		return d.uploadRequest(ctx, http.MethodPut, "/uploads/{id}/parts/{partNo}", func(req *resty.Request) {
			req.SetPathParam("id", uploadID)
			req.SetPathParam("partNo", strconv.Itoa(partNo))
			req.SetHeader("Content-Type", "application/octet-stream")
			// Deliberately not SetContentLength: the upload client's pre-request
			// hook promotes this header onto the raw request instead, so the body
			// streams rather than being buffered whole.
			req.SetHeader("Content-Length", strconv.FormatInt(size, 10))
			if checksum != "" {
				req.SetHeader("X-Part-Checksum", checksum)
			}
			req.SetBody(driver.NewLimitedUploadStream(ctx, reader))
		}, part)
	},
		retry.Context(ctx),
		retry.RetryIf(retryable),
		retry.Attempts(uploadMaxRetries),
		retry.DelayType(retry.BackOffDelay),
		retry.Delay(time.Second),
	)
}

// sessionMatchSlack absorbs the second-level rounding the server applies to
// modTime, so a resumed session still matches the file it was opened for.
const sessionMatchSlack = time.Second

// getOrCreateSession reuses an interrupted upload instead of starting over.
//
// A session survives a failed transfer (the server keeps it until its own
// cleanup job expires it), and every part already stored under it can be
// skipped, so an upload that died on its last part does not re-send the ones
// before it. Both first-party clients do this; teldrive's own web UI calls the
// same two endpoints.
//
// Matching is on parent, name and size. That alone is not proof of identity, so
// safety comes from re-hashing each candidate part and comparing it with the
// checksum the server recorded before anything is skipped. When hashing is off
// there is nothing to compare, so mtime has to carry the identity instead and
// is required to match too.
//
// mtime is deliberately not part of the match otherwise: OpenList only knows it
// from a Last-Modified header (server/handles/fsup.go), and a client that omits
// one gets time.Now(), which differs on every attempt and would make resuming
// impossible for exactly the uploads that need it most.
func (d *TeldriveV2) getOrCreateSession(ctx context.Context, parentID string, file model.FileStreamer, body base.Json) (*UploadSession, bool, error) {
	modTime := file.ModTime().UTC()
	var cursor string
	for {
		resp := &UploadSessionList{}
		if err := d.request(ctx, http.MethodGet, "/uploads", func(req *resty.Request) {
			params := map[string]string{"state": UploadOpen, "limit": strconv.Itoa(listPageSize)}
			if cursor != "" {
				params["cursor"] = cursor
			}
			req.SetQueryParams(params)
		}, resp); err != nil {
			// Resuming is an optimisation; a listing failure must not stop the
			// upload, so fall through to creating a fresh session.
			break
		}
		for i := range resp.Items {
			s := &resp.Items[i]
			if s.State != UploadOpen || s.ParentID != parentID || s.Name != file.GetName() {
				continue
			}
			if s.ExpectedSize != file.GetSize() || s.PartSize <= 0 {
				continue
			}
			if !d.HashEnabled {
				diff := s.ModTime.UTC().Sub(modTime)
				if diff > sessionMatchSlack || diff < -sessionMatchSlack {
					continue
				}
			}
			if !s.ExpiresAt.IsZero() && !s.ExpiresAt.After(time.Now()) {
				continue
			}
			return s, true, nil
		}
		if resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}

	session := &UploadSession{}
	if err := d.request(ctx, http.MethodPost, "/uploads", idempotent(func(req *resty.Request) {
		req.SetBody(body)
	}), session); err != nil {
		return nil, false, err
	}
	return session, false, nil
}

// storedParts indexes the parts the server already holds for a session.
func (d *TeldriveV2) storedParts(ctx context.Context, uploadID string) map[int]UploadPart {
	parts := make(map[int]UploadPart)
	var cursor string
	for {
		resp := &UploadPartList{}
		if err := d.request(ctx, http.MethodGet, "/uploads/{id}/parts", func(req *resty.Request) {
			req.SetPathParam("id", uploadID)
			params := map[string]string{"limit": strconv.Itoa(listPageSize)}
			if cursor != "" {
				params["cursor"] = cursor
			}
			req.SetQueryParams(params)
		}, resp); err != nil {
			// Same reasoning as above: worst case we re-upload everything.
			return parts
		}
		for _, p := range resp.Items {
			if p.State == UploadPartStored {
				parts[p.PartNo] = p
			}
		}
		if resp.NextCursor == "" {
			return parts
		}
		cursor = resp.NextCursor
	}
}
