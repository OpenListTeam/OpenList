// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/gofakes3"
	log "github.com/sirupsen/logrus"
)

// Compile-time assertions that s3Backend implements both the base Backend and
// the optional MultipartBackend interface from gofakes3.
var (
	_ gofakes3.Backend          = (*s3Backend)(nil)
	_ gofakes3.MultipartBackend = (*s3Backend)(nil)
)

// multipartPart records a single uploaded part on disk.
type multipartPart struct {
	path    string
	size    int64
	md5hex  string // unquoted lowercase hex
	updated time.Time
}

// multipartState tracks one in-progress multipart upload.
//
// gofakes3 does not serialize multipart operations for a given UploadID, so
// the parts map is guarded by mu. Each part is written to its own file inside
// dir, so concurrent UploadPart calls for different part numbers are safe.
type multipartState struct {
	bucket  string
	object  string
	meta    map[string]string
	dir     string
	created time.Time

	mu    sync.Mutex
	parts map[int]*multipartPart
}

// CreateMultipartUpload begins a new multipart upload. Parts are streamed to
// local temp files so that large uploads do not need to be buffered in memory.
//
// It implements gofakes3.MultipartBackend.
func (b *s3Backend) CreateMultipartUpload(ctx context.Context, bucket, object string, meta map[string]string) (gofakes3.UploadID, error) {
	if _, err := getBucketByName(bucket); err != nil {
		return "", err
	}

	tempDir := conf.Conf.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	dir, err := os.MkdirTemp(tempDir, "s3-multipart-*")
	if err != nil {
		return "", fmt.Errorf("create multipart upload dir: %w", err)
	}

	uploadID := gofakes3.UploadID(strings.ReplaceAll(uuid.NewString(), "-", ""))
	state := &multipartState{
		bucket:  bucket,
		object:  object,
		meta:    meta,
		dir:     dir,
		created: time.Now(),
		parts:   map[int]*multipartPart{},
	}

	b.uploads.Store(uploadID, state)
	log.Debugf("s3 multipart: created upload %s for %s/%s", uploadID, bucket, object)
	return uploadID, nil
}

// UploadPart writes a single part to disk and returns its (quoted) MD5 etag.
//
// It implements gofakes3.MultipartBackend. The body must contain exactly
// contentLength bytes; a short read is reported as ErrIncompleteBody so a
// truncated client request is never silently stored.
func (b *s3Backend) UploadPart(ctx context.Context, bucket, object string, uploadID gofakes3.UploadID, partNumber int, contentLength int64, body io.Reader) (string, error) {
	if partNumber <= 0 || partNumber > gofakes3.MaxUploadPartNumber {
		return "", gofakes3.ErrInvalidPart
	}

	val, ok := b.uploads.Load(uploadID)
	if !ok {
		return "", gofakes3.ErrNoSuchUpload
	}
	state := val.(*multipartState)

	partPath := filepath.Join(state.dir, fmt.Sprintf("part-%05d", partNumber))
	f, err := os.Create(partPath)
	if err != nil {
		return "", fmt.Errorf("create part file: %w", err)
	}
	// Remove a half-written file on any failure path.
	partFailed := true
	defer func() {
		if partFailed {
			_ = f.Close()
			_ = os.Remove(partPath)
		}
	}()

	hash := md5.New()
	// io.TeeReader feeds the hasher while the part is streamed to disk, so the
	// etag costs no extra pass over the data.
	n, err := utils.CopyWithBuffer(io.MultiWriter(f, hash), io.LimitReader(body, contentLength))
	if err != nil {
		return "", fmt.Errorf("write part %d: %w", partNumber, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close part %d: %w", partNumber, err)
	}
	if n != contentLength {
		// The client under-delivered (truncated/aborted request). gofakes3 does
		// not validate this for streaming backends, so we must.
		return "", gofakes3.ErrIncompleteBody
	}

	md5hex := hex.EncodeToString(hash.Sum(nil))
	etag := fmt.Sprintf("%q", md5hex)

	state.mu.Lock()
	if old := state.parts[partNumber]; old != nil && old.path != partPath {
		_ = os.Remove(old.path)
	}
	state.parts[partNumber] = &multipartPart{
		path:    partPath,
		size:    n,
		md5hex:  md5hex,
		updated: time.Now(),
	}
	state.mu.Unlock()

	partFailed = false
	log.Debugf("s3 multipart: stored part %d for %s (%d bytes)", partNumber, uploadID, n)
	return etag, nil
}

// CompleteMultipartUpload assembles the uploaded parts in ascending part-number
// order and streams the result into storage via the shared putStream path.
//
// It implements gofakes3.MultipartBackend. Part ordering and etags are
// validated against the parts actually received.
func (b *s3Backend) CompleteMultipartUpload(ctx context.Context, bucket, object string, uploadID gofakes3.UploadID, input *gofakes3.CompleteMultipartUploadRequest) (gofakes3.VersionID, string, error) {
	val, ok := b.uploads.Load(uploadID)
	if !ok {
		return "", "", gofakes3.ErrNoSuchUpload
	}
	state := val.(*multipartState)

	if input == nil || len(input.Parts) == 0 {
		return "", "", gofakes3.ErrorMessagef(gofakes3.ErrMalformedXML, "complete multipart upload has no parts")
	}
	// S3 requires the parts in a CompleteMultipartUpload request to be listed
	// in ascending part-number order.
	for i := 1; i < len(input.Parts); i++ {
		if input.Parts[i].PartNumber <= input.Parts[i-1].PartNumber {
			return "", "", gofakes3.ErrInvalidPartOrder
		}
	}

	// Validate every requested part exists with a matching etag, and collect
	// them in the order requested by the client (which is sorted ascending).
	state.mu.Lock()
	ordered := make([]*multipartPart, 0, len(input.Parts))
	var concat []byte
	for _, p := range input.Parts {
		stored := state.parts[p.PartNumber]
		if stored == nil {
			state.mu.Unlock()
			return "", "", gofakes3.ErrorMessagef(gofakes3.ErrInvalidPart, "unexpected part number %d in complete request", p.PartNumber)
		}
		if strings.Trim(p.ETag, "\"") != stored.md5hex {
			state.mu.Unlock()
			return "", "", gofakes3.ErrorMessagef(gofakes3.ErrInvalidPart, "unexpected part etag for number %d in complete request", p.PartNumber)
		}
		ordered = append(ordered, stored)
		// S3 multipart etag = hex(md5(concat(part_md5_digests)))-N
		concat = append(concat, stored.md5Bytes()...)
	}
	// Hold the lock until the part files are opened so an abort racing with
	// complete cannot delete them out from under us.
	readers := make([]io.Reader, 0, len(ordered))
	closers := make([]io.Closer, 0, len(ordered))
	var total int64
	for _, part := range ordered {
		f, err := os.Open(part.path)
		if err != nil {
			for _, c := range closers {
				_ = c.Close()
			}
			state.mu.Unlock()
			return "", "", fmt.Errorf("open part %s: %w", part.path, err)
		}
		readers = append(readers, f)
		closers = append(closers, f)
		total += part.size
	}
	state.mu.Unlock()

	combined := utils.NewReadCloser(io.MultiReader(readers...), func() error {
		var firstErr error
		for _, c := range closers {
			if err := c.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	})

	err := b.putStream(ctx, bucket, object, state.meta, combined, total)
	_ = combined.Close()
	if err != nil {
		// Leave the upload in place so the client may retry completion, per
		// the gofakes3 MultipartBackend contract.
		return "", "", err
	}

	// Success: drop bookkeeping and clean up part files.
	b.removeUpload(uploadID)

	sum := md5.Sum(concat)
	etag := fmt.Sprintf("%q", fmt.Sprintf("%s-%d", hex.EncodeToString(sum[:]), len(ordered)))
	log.Debugf("s3 multipart: completed upload %s -> %s/%s (%d bytes)", uploadID, bucket, object, total)
	return "", etag, nil
}

// AbortMultipartUpload discards an in-progress upload and its parts.
//
// It implements gofakes3.MultipartBackend and is idempotent: aborting an
// unknown upload succeeds so retries do not fail.
func (b *s3Backend) AbortMultipartUpload(ctx context.Context, bucket, object string, uploadID gofakes3.UploadID) error {
	b.removeUpload(uploadID)
	return nil
}

// removeUpload deletes the upload's temp directory and drops its bookkeeping.
// Missing uploads are ignored to keep abort/complete idempotent.
func (b *s3Backend) removeUpload(uploadID gofakes3.UploadID) {
	val, ok := b.uploads.LoadAndDelete(uploadID)
	if !ok {
		return
	}
	state := val.(*multipartState)
	if state.dir != "" {
		if err := os.RemoveAll(state.dir); err != nil {
			log.Warnf("s3 multipart: failed to clean up %s: %v", state.dir, err)
		}
	}
}

// md5Bytes returns the raw 16-byte MD5 digest of the part.
func (p *multipartPart) md5Bytes() []byte {
	b, _ := hex.DecodeString(p.md5hex)
	return b
}
