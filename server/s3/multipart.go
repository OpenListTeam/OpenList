// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
// Concurrency: gofakes3 does not serialize operations for the same uploadID,
// so concurrent UploadPart/Complete/Abort calls may overlap. The parts map is
// protected by mu. Each part is written to its own file inside dir, so
// concurrent UploadPart calls for different part numbers are safe without
// additional locking. lastActivity is updated under mu on create and on each
// part upload so the reaper can make a consistent expiry decision.
type multipartState struct {
	bucket       string
	object       string
	meta         map[string]string
	dir          string
	created      time.Time
	lastActivity time.Time // updated under mu on create and each part upload

	mu    sync.Mutex
	parts map[int]*multipartPart
	// completing is set while CompleteMultipartUpload is publishing the parts.
	// An abort must not delete the part files out from under that assembly, and
	// a second Complete must not assemble them a second time.
	completing bool
	// removed is set once the upload has been dropped from the map. A caller
	// that got past uploadFor before the removal happened must not go on to
	// publish parts whose directory the reaper has already deleted.
	removed bool
	// inflight counts the part uploads streaming right now. lastActivity is
	// stamped when a part starts and when it finishes, so without the count a
	// part that takes longer than the TTL looks abandoned for its whole
	// duration and the reaper deletes the directory under the writer.
	inflight int
}

// maxMultipartUploads bounds how many uploads one process tracks at a time.
// (A variable so that a test can pin it low.)
// Creating one is cheap - a map entry and a temp directory - and nothing is
// reclaimed before the TTL, so without a bound a client holding valid
// credentials can create them in a loop and take out the process with memory
// and inodes. The limit is far above what a working deployment needs.
var maxMultipartUploads int64 = 2000

// CreateMultipartUpload begins a new multipart upload. Parts are streamed to
// local temp files so that large uploads do not need to be buffered in memory.
//
// It implements gofakes3.MultipartBackend.
func (b *s3Backend) CreateMultipartUpload(ctx context.Context, bucket, object string, meta map[string]string) (gofakes3.UploadID, error) {
	if _, err := getBucketByName(bucket); err != nil {
		return "", err
	}
	if b.uploadCount.Add(1) > maxMultipartUploads {
		b.uploadCount.Add(-1)
		log.Warnf("s3 multipart: refusing a new upload, %d are already in progress", maxMultipartUploads)
		return "", gofakes3.ErrorMessagef(gofakes3.ErrorCode("SlowDown"),
			"too many multipart uploads in progress; complete or abort some before starting a new one")
	}
	released := false
	defer func() {
		if !released {
			b.uploadCount.Add(-1)
		}
	}()

	tempDir := conf.Conf.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	dir, err := os.MkdirTemp(tempDir, "s3-multipart-*")
	if err != nil {
		return "", fmt.Errorf("create multipart upload dir: %w", err)
	}

	uploadID := gofakes3.UploadID(strings.ReplaceAll(uuid.NewString(), "-", ""))
	now := time.Now()
	state := &multipartState{
		bucket:       bucket,
		object:       object,
		meta:         meta,
		dir:          dir,
		created:      now,
		lastActivity: now,
		parts:        map[int]*multipartPart{},
	}

	b.uploads.Store(uploadID, state)
	// The count is now owned by the map entry; every path that removes the
	// entry releases it.
	released = true
	log.Debugf("s3 multipart: created upload %s for %s/%s", uploadID, bucket, object)
	return uploadID, nil
}

// uploadFor returns the state of an in-progress upload, provided it was created
// for exactly this bucket and object.
//
// An upload ID is a bearer token: gofakes3 derives the bucket and the key from
// the URL of whichever step is being performed, so without this check a key
// allowed to write one bucket could append parts to another writer's upload and
// complete it into an object it was never allowed to touch.
func (b *s3Backend) uploadFor(uploadID gofakes3.UploadID, bucket, object string) (*multipartState, error) {
	val, ok := b.uploads.Load(uploadID)
	if !ok {
		return nil, gofakes3.ErrNoSuchUpload
	}
	state := val.(*multipartState)
	if state.bucket != bucket || state.object != object {
		log.Warnf("s3 multipart: upload %s belongs to %s/%s, refused from %s/%s",
			uploadID, state.bucket, state.object, bucket, object)
		return nil, gofakes3.ErrNoSuchUpload
	}
	return state, nil
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

	state, err := b.uploadFor(uploadID, bucket, object)
	if err != nil {
		return "", err
	}

	if contentLength > maxUploadPartSize {
		// S3 caps a single part at 5 GiB; without the check a signed request
		// could make the gateway stream an unbounded body into the temp dir.
		return "", gofakes3.ErrorMessagef(gofakes3.ErrorCode("EntityTooLarge"),
			"part %d is %d bytes; the largest allowed part size is %d bytes", partNumber, contentLength, maxUploadPartSize)
	}

	// Mark the upload as active before streaming: a part upload can take longer
	// than the reaper's TTL (a slow client resuming a large upload), and the
	// reaper would otherwise delete the temp directory mid-write.
	state.mu.Lock()
	if state.completing || state.removed {
		state.mu.Unlock()
		// The upload is being published or has been dropped; S3 reports it as
		// gone rather than accepting parts nobody will assemble.
		return "", gofakes3.ErrNoSuchUpload
	}
	state.lastActivity = time.Now()
	// Count the part as in flight for the whole stream: the reaper only looks
	// at lastActivity, which is stamped here, so a part slower than the TTL
	// would otherwise be reaped while it is being written.
	state.inflight++
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.inflight--
		state.lastActivity = time.Now()
		state.mu.Unlock()
	}()

	partPath := filepath.Join(state.dir, fmt.Sprintf("part-%05d", partNumber))
	// The part is streamed into a temporary file and only renamed into place
	// once it is complete. Writing straight to partPath truncated the part that
	// had already been accepted, so an SDK retrying a failed upload with the
	// same part number destroyed the good copy and then removed it, and the
	// completion of the upload read a part that no longer existed.
	f, err := os.CreateTemp(state.dir, fmt.Sprintf("part-%05d-*.tmp", partNumber))
	if err != nil {
		return "", fmt.Errorf("create part file: %w", err)
	}
	tmpPath := f.Name()
	// Remove the half-written temporary file on any failure path; the previous
	// part stays where it is.
	partFailed := true
	defer func() {
		if partFailed {
			_ = f.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	partMD5 := md5.New()
	// gofakes3 folds X-Amz-Content-Sha256 into the signature but never hashes a
	// part body, and the verifier contentSha256Guard installs only reports its
	// verdict once it reaches the end of the body - which never happens on this
	// path, because the bytes are read through a LimitReader sized from the
	// declared length. A part whose payload was substituted between signing and
	// delivery was therefore stored, given an ETag computed from the substituted
	// bytes, assembled at Complete and served back with a 200 at every step. A
	// whole-object PUT is verified inside putStream, which hashes what the
	// driver actually received; the part needs the same treatment, so the digest
	// is finished here over exactly the bytes written to disk.
	declared := declaredPayloadHash(ctx)
	var declaredSum hash.Hash
	writers := []io.Writer{f, partMD5}
	if declared != "" {
		declaredSum = sha256.New()
		writers = append(writers, declaredSum)
	}
	// The hashers are fed while the part is streamed to disk, so neither digest
	// costs an extra pass over the data.
	//
	// The reader is wrapped so that a stream that breaks on the client side is
	// still recognisable after the copy loop returned: a frame that does not
	// decode reaches the loop as a plain read error, exactly like a failing
	// disk would, and the two must not share an answer.
	bodyErr := &bodyErrorReader{inner: io.LimitReader(body, contentLength)}
	n, err := utils.CopyWithBuffer(io.MultiWriter(writers...), bodyErr)
	if err != nil {
		// gofakes3 verifies Content-MD5 by handing us a hashing reader, so a
		// wrong digest arrives here as a typed error. Its error writer matches
		// on the concrete type, so wrapping it would turn the expected 400
		// BadDigest into a 500 the SDK retries.
		if code, ok := asS3ErrorCode(err); ok {
			return "", code
		}
		if errors.Is(err, errPayloadHashMismatch) {
			// Declared payload hash disagreed with the bytes: a client error, not
			// a server fault, so it must not be wrapped into a 500 the SDK
			// retries with the same wrong body.
			return "", asS3WriteError(err)
		}
		if bodyErr.err != nil {
			// The part stream itself broke - a chunk frame that does not decode,
			// a body that ends inside a frame, a connection that died mid-part.
			// That is the request's fault, and gofakes3 renders an unclassified
			// error as 500 InternalError, which tells every SDK to retry a body
			// it can never deliver. S3 answers 400 for exactly this case; a body
			// error that already carries an S3 code keeps its own answer.
			if _, classified := asS3ErrorCode(bodyErr.err); !classified {
				return "", gofakes3.ErrIncompleteBody
			}
			return "", asS3WriteError(bodyErr.err)
		}
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
	if watcher := chunkWatcherFromContext(ctx); watcher != nil {
		// The framed body is drained to its end here, which also reaches the
		// trailer: that is where a streaming upload carries the checksum it asks
		// the gateway to verify.
		switch {
		case watcher.overrunAfterDecodedBody():
			return "", gofakes3.ErrIncompleteBody
		case watcher.trailerChecksumMismatch():
			// A part that declares its checksum in the trailer is verified like a
			// whole-object PUT: the value the client asked to be checked is either
			// honoured or refused, never silently stored.
			return "", gofakes3.ErrBadDigest
		}
	}
	if declaredSum != nil && hex.EncodeToString(declaredSum.Sum(nil)) != declared {
		// The bytes disagree with the hash the request was signed with: a client
		// error (400 BadDigest), the same answer a whole-object PUT gets.
		return "", asS3WriteError(errPayloadHashMismatch)
	}
	md5hex := hex.EncodeToString(partMD5.Sum(nil))
	etag := fmt.Sprintf("%q", md5hex)

	// Commit under the lock: the rename and the bookkeeping have to be one step,
	// otherwise two writers of the same part number can rename in one order and
	// record in the other, leaving an etag that describes bytes nobody stored.
	now := time.Now()
	state.mu.Lock()
	if state.completing || state.removed {
		state.mu.Unlock()
		// A completion started while this part was streaming; it validated the
		// parts it found and is assembling them now. Accepting this part would
		// mean publishing bytes the client was told were stored but that the
		// completion never saw.
		return "", gofakes3.ErrNoSuchUpload
	}
	if err := os.Rename(tmpPath, partPath); err != nil {
		state.mu.Unlock()
		return "", fmt.Errorf("commit part %d: %w", partNumber, err)
	}
	if old := state.parts[partNumber]; old != nil && old.path != partPath {
		_ = os.Remove(old.path)
	}
	state.parts[partNumber] = &multipartPart{
		path:    partPath,
		size:    n,
		md5hex:  md5hex,
		updated: now,
	}
	state.lastActivity = now
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
	state, err := b.uploadFor(uploadID, bucket, object)
	if err != nil {
		return "", "", err
	}

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
	if state.completing || state.removed {
		state.mu.Unlock()
		// Somebody else is publishing this upload right now; the loser of that
		// race must report the upload as gone rather than assemble it twice.
		return "", "", gofakes3.ErrNoSuchUpload
	}
	ordered := make([]*multipartPart, 0, len(input.Parts))
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
	}
	// Collect the file names and mark the upload as being published, so an
	// abort racing with this completion leaves the parts alone.
	paths := make([]string, 0, len(ordered))
	var total int64
	for _, part := range ordered {
		paths = append(paths, part.path)
		total += part.size
	}
	// putStream refuses content for a key ending in "/" only when it is told the
	// size, and a completion hands it -1: the request body is the XML part list
	// and says nothing about the bytes being assembled. The check therefore has
	// to happen here. Without it the completion of a key such as "folder/"
	// answered 200 with a composite ETag for the uploaded bytes while putStream
	// took the key for a directory marker and stored nothing: every byte was
	// dropped, an empty folder was left in its place, and a read of the key
	// returned an empty object with no ETag at all.
	if strings.HasSuffix(object, "/") && total > 0 {
		state.mu.Unlock()
		return "", "", gofakes3.ErrorMessage(gofakes3.ErrInvalidArgument,
			"a key ending in \"/\" cannot hold content")
	}
	state.completing = true
	state.mu.Unlock()

	// The parts are opened one at a time as the reader advances: S3 allows
	// 10000 parts, and opening them all at once would exhaust the file
	// descriptor budget of the process.
	partReader := &multipartPartsReader{paths: paths}
	combined := utils.NewReadCloser(partReader, partReader.Close)

	defer combined.Close()

	// Hash the bytes on the way in: the ETag of the finished object is the MD5
	// of its content, exactly what a later HEAD reports, so a conditional write
	// keeps comparing like with like.
	contentHash := md5.New()
	// The object is assembled from the parts, so the request body (the XML part
	// list) says nothing about the bytes that are about to be written; -1 as the
	// expected size and no declared hash keeps the verification off this path.
	err = b.putStream(withoutPayloadExpectation(ctx), bucket, object, state.meta,
		io.TeeReader(combined, contentHash), -1)
	if err != nil {
		// Leave the upload in place so the client may retry completion, per
		// the gofakes3 MultipartBackend contract; the parts are no longer being
		// published, so an abort is allowed again.
		state.mu.Lock()
		state.completing = false
		state.mu.Unlock()
		return "", "", err
	}

	// Success: drop bookkeeping and clean up part files.
	b.removeUpload(uploadID)

	// S3 gives a multipart object the composite ETag "md5(md5(part)...) -N"; a
	// plain MD5 of the assembled content is a different value, and clients that
	// verify a download against the ETag they were handed report a corrupt file.
	composite := md5.New()
	for _, part := range ordered {
		digest, decodeErr := hex.DecodeString(part.md5hex)
		if decodeErr != nil {
			return "", "", errors.New("a part carries an unreadable digest")
		}
		composite.Write(digest)
	}
	etag := fmt.Sprintf("\"%s-%d\"", hex.EncodeToString(composite.Sum(nil)), len(ordered))
	if bucketInfo, err := getBucketByName(bucket); err == nil {
		if fp, err := objectFSPath(bucketInfo.Path, object); err == nil {
			b.setStoredETag(ctx, fp, etag)
		}
	}
	log.Debugf("s3 multipart: completed upload %s -> %s/%s (%d bytes)", uploadID, bucket, object, total)
	return "", etag, nil
}

// multipartPartsReader streams the parts of one upload in order, opening each
// part file only when the reader reaches it.
type multipartPartsReader struct {
	paths []string
	next  int
	cur   *os.File
}

func (m *multipartPartsReader) Read(p []byte) (int, error) {
	for {
		if m.cur == nil {
			if m.next >= len(m.paths) {
				return 0, io.EOF
			}
			f, err := os.Open(m.paths[m.next])
			if err != nil {
				return 0, fmt.Errorf("open part %s: %w", m.paths[m.next], err)
			}
			m.cur = f
			m.next++
		}
		n, err := m.cur.Read(p)
		if n > 0 {
			return n, nil
		}
		if err == nil {
			continue
		}
		_ = m.cur.Close()
		m.cur = nil
		if err != io.EOF {
			return 0, err
		}
	}
}

func (m *multipartPartsReader) Close() error {
	if m.cur == nil {
		return nil
	}
	err := m.cur.Close()
	m.cur = nil
	return err
}

// AbortMultipartUpload discards an in-progress upload and its parts.
//
// It implements gofakes3.MultipartBackend and is idempotent: aborting an
// unknown upload succeeds so retries do not fail.
func (b *s3Backend) AbortMultipartUpload(ctx context.Context, bucket, object string, uploadID gofakes3.UploadID) error {
	// Abort is idempotent for the owner of the upload, but a caller that does
	// not own it must not be able to cancel it, so the ownership check decides
	// and a mismatch is reported exactly like a missing upload.
	if _, err := b.uploadFor(uploadID, bucket, object); err != nil {
		return nil
	}
	// An abort racing with a completion that is already streaming the parts
	// must not delete them: the object would be published truncated. The
	// condition is re-checked while the removal happens, so a completion that
	// starts in between cannot be defeated.
	if !b.removeUploadIf(uploadID, nil) {
		if _, stillThere := b.uploads.Load(uploadID); stillThere {
			log.Warnf("s3 multipart: abort of %s ignored, a completion is in progress", uploadID)
		}
	}
	return nil
}

// removeUpload deletes the upload's temp directory and drops its bookkeeping.
// Missing uploads are ignored to keep abort/complete idempotent.
//
// It is called by a completion that already holds the upload's lock protocol
// (completing set), so the removal itself is unconditional.
func (b *s3Backend) removeUpload(uploadID gofakes3.UploadID) {
	val, ok := b.uploads.Load(uploadID)
	if !ok {
		return
	}
	state := val.(*multipartState)
	state.mu.Lock()
	state.removed = true
	b.uploads.Delete(uploadID)
	b.uploadCount.Add(-1)
	dir := state.dir
	state.mu.Unlock()
	if dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			log.Warnf("s3 multipart: failed to clean up %s: %v", dir, err)
		}
	}
}

// removeUploadIf drops an upload and its part files, but only when the upload
// is still idle in the way the caller expects: the test, the removal from the
// map and the directory cleanup all happen while holding the upload's lock.
//
// Checking first and deleting afterwards let the reaper delete the part files
// of an upload that had just started a slow UploadPart, and let an abort delete
// them while a completion was publishing - both left the client with a 500 and
// no parts to retry from. cond runs under the lock, so it must not take it.
func (b *s3Backend) removeUploadIf(uploadID gofakes3.UploadID, cond func(*multipartState) bool) bool {
	val, ok := b.uploads.Load(uploadID)
	if !ok {
		return false
	}
	state := val.(*multipartState)
	state.mu.Lock()
	if state.removed || state.completing || (cond != nil && !cond(state)) {
		state.mu.Unlock()
		return false
	}
	state.removed = true
	b.uploads.Delete(uploadID)
	b.uploadCount.Add(-1)
	dir := state.dir
	state.mu.Unlock()
	if dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			log.Warnf("s3 multipart: failed to clean up %s: %v", dir, err)
		}
	}
	return true
}

// md5Bytes returns the raw 16-byte MD5 digest of the part.
func (p *multipartPart) md5Bytes() []byte {
	b, _ := hex.DecodeString(p.md5hex)
	return b
}

// Defaults for reaping abandoned multipart uploads. A client that never sends
// CompleteMultipartUpload or AbortMultipartUpload would otherwise leave part
// files on disk forever; the reaper drops uploads inactive for longer than the
// TTL.
const (
	defaultMultipartTTL = 24 * time.Hour
	multipartDirPrefix  = "s3-multipart-"

	// maxUploadPartSize is S3's ceiling for one part of a multipart upload. The
	// matching floor (5 MiB for every part but the last) is deliberately NOT
	// enforced: this gateway has always accepted small parts, and rejecting them
	// would break clients that upload happily today for the sake of an error
	// message no client acts on. The ceiling is different - it bounds how much a
	// single request can make the gateway write to its temp dir.
	maxUploadPartSize = 5 << 30
)

// multipartTTL returns the configured max idle time for an upload before the
// reaper reclaims it. It parses conf.Conf.S3.MultipartTTL as a Go duration
// (e.g. "24h", "30m"); an empty or invalid value falls back to the default.
func multipartTTL() time.Duration {
	if v := conf.Conf.S3.MultipartTTL; v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultMultipartTTL
}

// reapInterval derives the reaper tick interval from the TTL: a quarter of the
// TTL, clamped to [10s, 1h].
func reapInterval(ttl time.Duration) time.Duration {
	d := ttl / 4
	if d < 10*time.Second {
		d = 10 * time.Second
	}
	if d > time.Hour {
		d = time.Hour
	}
	return d
}

// startReaper removes leftover part directories from a previous process crash
// and then launches a background goroutine that periodically reclaims uploads
// inactive for longer than the TTL. The goroutine runs for the lifetime of the
// process; NewServer is called once at startup, so there is one reaper per
// backend instance.
func (b *s3Backend) startReaper() {
	b.cleanupStaleDirs(time.Now(), multipartTTL())
	interval := reapInterval(multipartTTL())
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for now := range ticker.C {
			b.reapExpired(now, multipartTTL())
		}
	}()
}

// reapExpired removes uploads whose lastActivity is older than ttl. It is safe
// to call concurrently with UploadPart/Complete/Abort: each candidate is
// re-checked under its own lock and removed atomically via removeUpload.
func (b *s3Backend) reapExpired(now time.Time, ttl time.Duration) {
	b.uploads.Range(func(key, val any) bool {
		state := val.(*multipartState)
		uploadID := key.(gofakes3.UploadID)
		reaped := b.removeUploadIf(uploadID, func(s *multipartState) bool {
			return s.inflight == 0 && now.Sub(s.lastActivity) > ttl
		})
		if reaped {
			log.Infof("s3 multipart: reaped abandoned upload %s (%s/%s)", uploadID, state.bucket, state.object)
		}
		return true
	})
}

// cleanupStaleDirs removes s3-multipart-* directories under TempDir that are
// older than ttl. This reclaims part files left behind by a previous process
// crash; dirs younger than ttl are left alone so a concurrently-starting
// sibling backend instance is never disturbed.
func (b *s3Backend) cleanupStaleDirs(now time.Time, ttl time.Duration) {
	tempDir := conf.Conf.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return
	}
	cutoff := now.Add(-ttl)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), multipartDirPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(tempDir, e.Name())); err != nil {
			log.Warnf("s3 multipart: failed to clean up stale dir %s: %v", e.Name(), err)
		} else {
			log.Infof("s3 multipart: removed stale multipart dir %s", e.Name())
		}
	}
}

// listPartsResult is the document S3 answers a ListParts request with.
type listPartsResult struct {
	XMLName              xml.Name           `xml:"ListPartsResult"`
	Bucket               string             `xml:"Bucket"`
	Key                  string             `xml:"Key"`
	UploadID             string             `xml:"UploadId"`
	StorageClass         string             `xml:"StorageClass"`
	PartNumberMarker     int                `xml:"PartNumberMarker"`
	NextPartNumberMarker int                `xml:"NextPartNumberMarker"`
	MaxParts             int                `xml:"MaxParts"`
	IsTruncated          bool               `xml:"IsTruncated"`
	Parts                []listPartsListing `xml:"Part"`
}

// listPartsListing is one <Part> element of a ListParts document.
type listPartsListing struct {
	PartNumber   int    `xml:"PartNumber"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

// listPartsHandler answers ListParts from the upload's own state.
//
// gofakes3 keeps a second, private list of the parts of an upload and only adds
// to it when its own part-upload handler runs. UploadPartCopy is implemented in
// front of gofakes3 and writes the part straight into the backend, so a copied
// part exists (Complete validates and assembles it) while ListParts answers the
// tracked subset only: an upload made entirely of copied parts is listed as
// empty, and one that mixes them reports fewer parts and a smaller total size
// than Complete will publish. A client resuming an interrupted server-side copy
// therefore re-copies parts it already has, and any tool that sizes an upload
// from ListParts before completing it sees a total that omits the copies.
//
// The backend is the record every part write goes through, so the listing is
// served from it. An upload id the backend does not know for exactly this bucket
// and key falls through to gofakes3, which keeps deciding what an unknown upload
// looks like.
func listPartsHandler(next http.Handler, backend *s3Backend) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		query := r.URL.Query()
		uploadIDs, ok := query["uploadId"]
		if !ok || len(uploadIDs) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		bucket, object, isObjectPath := parseObjectPath(r.URL.Path)
		if !isObjectPath || bucket == "" || object == "" {
			next.ServeHTTP(w, r)
			return
		}
		uploadID := gofakes3.UploadID(strings.TrimSpace(uploadIDs[0]))
		state, err := backend.uploadFor(uploadID, bucket, object)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		marker := 0
		if raw := strings.TrimSpace(query.Get("part-number-marker")); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed < 0 || parsed > 10000 {
				writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument", bucket,
					"part-number-marker must be an integer between 0 and 10000.")
				return
			}
			marker = parsed
		}
		maxParts := 1000 // S3's default and its ceiling
		if raw := strings.TrimSpace(query.Get("max-parts")); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			// Out-of-range values used to be passed on to gofakes3 or clamped
			// locally: 0 selected no part yet still flagged the page as truncated,
			// which indexed the last selected part of an empty slice and answered
			// 500 InternalError - the one status every SDK retries, so the client
			// resends a request that can never succeed. S3 answers InvalidArgument.
			if parseErr != nil || parsed < 1 || parsed > 1000 {
				writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument", bucket,
					"max-parts must be an integer between 1 and 1000.")
				return
			}
			maxParts = parsed
		}

		state.mu.Lock()
		numbers := make([]int, 0, len(state.parts))
		for number := range state.parts {
			if number > marker {
				numbers = append(numbers, number)
			}
		}
		sort.Ints(numbers)
		var selected []listPartsListing
		for _, number := range numbers {
			if len(selected) >= maxParts {
				break
			}
			part := state.parts[number]
			selected = append(selected, listPartsListing{
				PartNumber:   number,
				LastModified: part.updated.UTC().Format("2006-01-02T15:04:05.000Z"),
				ETag:         fmt.Sprintf("%q", part.md5hex),
				Size:         part.size,
			})
		}
		state.mu.Unlock()

		result := listPartsResult{
			Bucket:       bucket,
			Key:          object,
			StorageClass: "STANDARD",
			MaxParts:     1000,
			Parts:        selected,
		}
		// S3 echoes the storage class of the upload and the paging state the
		// client sent, not the values this handler chose.
		result.UploadID = string(uploadID)
		result.PartNumberMarker = marker
		result.MaxParts = maxParts
		if len(numbers) > len(selected) {
			result.IsTruncated = true
			// An empty page (nothing after the marker) is not truncated; the
			// cursor only exists when a part was actually listed.
			if len(selected) > 0 {
				result.NextPartNumberMarker = selected[len(selected)-1].PartNumber
			}
		}

		payload, marshalErr := xml.MarshalIndent(result, "", "  ")
		if marshalErr != nil {
			writeS3Error(w, r, http.StatusInternalServerError, "InternalError", bucket,
				"The part listing could not be encoded.")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(xml.Header))
		_, _ = w.Write(payload)
	})
}
