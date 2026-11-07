// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"

	"github.com/OpenListTeam/OpenList/v4/internal/access"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/gofakes3"
	"github.com/google/uuid"
	"github.com/ncw/swift/v2"
	log "github.com/sirupsen/logrus"
)

var (
	// timeFormat has to be the HTTP date, not a lookalike: "Mon, 2 Jan 2006
	// 15:04:05 GMT" writes a single digit day, which http.ParseTime - and every
	// strict client - refuses to parse, so a Last-Modified the gateway emitted
	// could not be fed back as If-Modified-Since or as an If-Range date.
	timeFormat = http.TimeFormat
)

// s3TempKeyPrefix marks the sibling key an upload streams into before it is
// renamed over the real one. A key carrying it is never a user object: listing
// hides it and addressing it directly is refused.
const s3TempKeyPrefix = ".openlist-s3-tmp-"

// s3TempKeyName returns a collision-resistant temporary base name.
func s3TempKeyName() string {
	return s3TempKeyPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// isS3TempKey reports whether a key is one of those in-flight temporaries.
func isS3TempKey(objectName string) bool {
	return strings.HasPrefix(path.Base(strings.TrimSuffix(objectName, "/")), s3TempKeyPrefix)
}

// s3Backend implements the gofakes3.Backend interface to make an S3
// backend for gofakes3. It also implements gofakes3.MultipartBackend so that
// multipart uploads are streamed to local temp files part-by-part and
// assembled into storage on completion, instead of being buffered in memory.
type s3Backend struct {
	meta    *sync.Map
	listDir func(context.Context, string) ([]model.Obj, error)
	uploads *sync.Map // map[gofakes3.UploadID]*multipartState
	// uploadCount mirrors len(uploads) so that creating an upload does not have
	// to walk the map.
	uploadCount atomic.Int64
	access      accessTracker

	// metaMu guards metaSize, the number of entries in meta. The cache is
	// bounded (see cacheObjectMetadata) because it is keyed per object.
	metaMu   sync.Mutex
	metaSize int

	// keyLocks serialises content+metadata writes per object (see key_lock.go).
	keyLocks keyLocks

	// putPausedAfterContent is a test hook that runs between the content write
	// and the metadata write - the window the per-key lock protects. It is nil
	// everywhere but in tests.
	putPausedAfterContent func()

	// flushTestHook runs between collecting an access batch and writing it, the
	// window a concurrent delete slips into. Nil outside tests.
	flushTestHook func()
}

// liveBackends tracks the backends of this process so that a shutdown can write
// the reads still buffered in memory. They are created once per instance; the
// slice only exists for that final flush.
var liveBackends struct {
	mu  sync.Mutex
	all []*s3Backend
}

// FlushAccess writes the reads each backend has buffered. Without it a restart
// silently dropped up to one flush interval of them - and for a hot/cold split
// the read time is the whole point of the record.
func FlushAccess() {
	liveBackends.mu.Lock()
	backends := make([]*s3Backend, len(liveBackends.all))
	copy(backends, liveBackends.all)
	liveBackends.mu.Unlock()
	for _, b := range backends {
		b.flushAccess()
	}
}

// newBackend creates a new SimpleBucketBackend.
func newBackend() gofakes3.Backend {
	b := &s3Backend{
		meta:    new(sync.Map),
		uploads: new(sync.Map),
		listDir: getDirEntries,
	}
	b.startReaper()
	liveBackends.mu.Lock()
	liveBackends.all = append(liveBackends.all, b)
	liveBackends.mu.Unlock()
	return b
}

// ListBuckets always returns the default bucket.
func (b *s3Backend) ListBuckets(ctx context.Context) ([]gofakes3.BucketInfo, error) {
	buckets, err := getAndParseBuckets()
	if err != nil {
		return nil, err
	}
	var response []gofakes3.BucketInfo
	rule, _ := ctx.Value(s3AuthRuleContextKey).(*S3BucketAuthRule)

	for _, b := range buckets {
		if rule != nil && !rule.MatchesBucket(b.Name) {
			continue
		}
		node, _ := fs.Get(ctx, b.Path, &fs.GetArgs{})
		var creationDate time.Time
		if node != nil {
			creationDate = node.ModTime()
		}
		response = append(response, gofakes3.BucketInfo{
			// Name:         gofakes3.URLEncode(b.Name),
			Name:         b.Name,
			CreationDate: gofakes3.NewContentTime(creationDate),
		})
	}
	return response, nil
}

// ListBucket lists the objects in the given bucket.
func (b *s3Backend) ListBucket(ctx context.Context, bucketName string, prefix *gofakes3.Prefix, page gofakes3.ListBucketPage) (*gofakes3.ObjectList, error) {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return nil, err
	}
	bucketPath := bucket.Path

	if prefix == nil {
		// A fresh value, never the package-level one: the workaround below
		// mutates the struct it is given, and gofakes3 hands in its own pointer
		// for a real request. Sharing one object across requests was a data
		// race on a global, and the winner of it decided whether another
		// request's listing honoured its delimiter.
		prefix = &gofakes3.Prefix{}
	}

	// workaround
	if strings.TrimSpace(prefix.Prefix) == "" {
		prefix.HasPrefix = false
	}
	if strings.TrimSpace(prefix.Delimiter) == "" {
		prefix.HasDelimiter = false
	}

	path, remaining := prefixParser(prefix)

	response, err := b.listPage(ctx, bucketPath, path, remaining, prefix.HasDelimiter, prefix, page)
	if err == gofakes3.ErrNoSuchKey {
		// AWS just returns an empty list
		response = gofakes3.NewObjectList()
	} else if err != nil {
		return nil, err
	}

	return response, nil
}

// emptyDirMarkerObject serves the key a listing invents for a directory that
// holds nothing.
//
// S3 has no directories, so an empty one would vanish from a flat listing and a
// client could never delete it. The listing therefore reports
// "<dir>/ThisIsAnEmptyFolderInTheS3Bucket" - but nothing answered GET/HEAD for
// that key, so `aws s3 sync`, `rclone sync` and `mc mirror` read the listing,
// tried to fetch the object and failed the whole run with NoSuchKey. The marker
// exists for exactly as long as the directory stays empty.
func (b *s3Backend) emptyDirMarkerObject(ctx context.Context, bucketPath, objectName string) (*gofakes3.Object, bool) {
	if path.Base(objectName) != emptyObjectName {
		return nil, false
	}
	dirName := path.Dir(objectName)
	if dirName == "." || dirName == "/" {
		return nil, false
	}
	dirPath, err := objectFSPath(bucketPath, dirName)
	if err != nil {
		return nil, false
	}
	fmeta, _ := op.GetNearestMeta(dirPath)
	node, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), dirPath, &fs.GetArgs{})
	if err != nil || !node.IsDir() {
		return nil, false
	}
	entries, err := b.listDir(ctx, dirPath)
	if err != nil || len(entries) != 0 {
		return nil, false
	}
	return &gofakes3.Object{
		Name:     objectName,
		Size:     0,
		Contents: io.NopCloser(strings.NewReader("")),
		Metadata: map[string]string{
			"Last-Modified": node.ModTime().UTC().Format(timeFormat),
			"Content-Type":  "application/octet-stream",
		},
	}, true
}

// dirMarkerObject answers a read of a key that names a directory.
//
// A PUT of "prefix/" is accepted (it creates the directory) but nothing used to
// answer a GET/HEAD for that key, and the key never showed up in a listing: the
// write reported success and the object it claimed to create could neither be
// read nor listed. Real S3 stores a zero-byte object for such a key; the file
// system cannot hold a directory and an object on one path, so the directory
// stands in for it and the read is answered with the zero-byte body the client
// wrote. Only keys written with the trailing slash are affected - a plain
// "prefix" read stays NoSuchKey.
func (b *s3Backend) dirMarkerObject(objectName string, node model.Obj, markerRequest bool) (*gofakes3.Object, bool) {
	if !node.IsDir() || !(markerRequest || strings.HasSuffix(objectName, "/")) {
		return nil, false
	}
	return &gofakes3.Object{
		Name:     objectName,
		Size:     0,
		Contents: io.NopCloser(strings.NewReader("")),
		Metadata: map[string]string{
			"Last-Modified": node.ModTime().UTC().Format(timeFormat),
			"Content-Type":  "application/octet-stream",
		},
	}, true
}

// HeadObject returns the fileinfo for the given object name.
//
// Note that the metadata is not supported yet.
func (b *s3Backend) HeadObject(ctx context.Context, bucketName, objectName string) (*gofakes3.Object, error) {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return nil, err
	}
	bucketPath := bucket.Path

	fp, err := objectFSPath(bucketPath, objectName)
	if err != nil {
		return nil, gofakes3.KeyNotFound(objectName)
	}
	fmeta, _ := op.GetNearestMeta(fp)
	node, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), fp, &fs.GetArgs{})
	if err != nil {
		if obj, ok := b.emptyDirMarkerObject(ctx, bucketPath, objectName); ok {
			return obj, nil
		}
		return nil, gofakes3.KeyNotFound(objectName)
	}

	if node.IsDir() {
		if obj, ok := b.dirMarkerObject(objectName, node, isDirMarkerRequest(ctx)); ok {
			return obj, nil
		}
		return nil, gofakes3.KeyNotFound(objectName)
	}
	if isDirMarkerRequest(ctx) || strings.HasSuffix(objectName, "/") {
		// The request named "prefix/", which S3 keeps as a key of its own. Here
		// only the directory marker ever lives under it, so answering with the
		// same-named file described an object the client did not ask about.
		return nil, gofakes3.KeyNotFound(objectName)
	}
	// A HEAD is a metadata probe, not a read. Sync tools (rclone, the AWS CLI,
	// s3cmd) HEAD every object they copy or compare, so counting it as access
	// would mark the whole library hot and leave the hot/cold tiers nothing to
	// separate. Only a GET records access; HEAD reports the state without
	// changing it.

	size := node.GetSize()
	// hash := getFileHashByte(fobj)

	meta := map[string]string{
		"Last-Modified": node.ModTime().UTC().Format(timeFormat),
		"Content-Type":  utils.GetMimeType(fp),
	}

	if stored, ok := b.loadObjectMetadata(ctx, fp); ok {
		for k, v := range stored {
			meta[k] = v
		}
	}

	return &gofakes3.Object{
		Name: objectName,
		// Hash:     hash,
		ETag:     takeStoredETag(meta),
		Metadata: meta,
		Size:     size,
		Contents: noOpReadCloser{},
	}, nil
}

// GetObject fetchs the object from the filesystem.
func (b *s3Backend) GetObject(ctx context.Context, bucketName, objectName string, rangeRequest *gofakes3.ObjectRangeRequest) (s3Obj *gofakes3.Object, err error) {
	defer func() { err = mapBackendError(err) }()

	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return nil, err
	}
	bucketPath := bucket.Path

	fp, err := objectFSPath(bucketPath, objectName)
	if err != nil {
		return nil, gofakes3.KeyNotFound(objectName)
	}
	fmeta, _ := op.GetNearestMeta(fp)
	node, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), fp, &fs.GetArgs{})
	if err != nil {
		if obj, ok := b.emptyDirMarkerObject(ctx, bucketPath, objectName); ok {
			return obj, nil
		}
		return nil, gofakes3.KeyNotFound(objectName)
	}

	if node.IsDir() {
		if obj, ok := b.dirMarkerObject(objectName, node, isDirMarkerRequest(ctx)); ok {
			return obj, nil
		}
		return nil, gofakes3.KeyNotFound(objectName)
	}
	if isDirMarkerRequest(ctx) || strings.HasSuffix(objectName, "/") {
		// "prefix/" is a different key from "prefix": nothing but the directory
		// marker is ever stored under it, so this must not serve the file.
		return nil, gofakes3.KeyNotFound(objectName)
	}
	b.recordAccess(fp)

	link, file, err := fs.Link(ctx, fp, model.LinkArgs{})
	if err != nil {
		return nil, err
	}
	recordFileRead(bucketName, fp, file)
	defer func() {
		if s3Obj == nil {
			_ = link.Close()
		}
	}()

	size := link.ContentLength
	if size <= 0 {
		size = file.GetSize()
	}
	if rangeRequest != nil && ignoreRangeFromContext(ctx) {
		// A multi-range request is answered with the whole object: S3 drops the
		// header rather than failing the read, and gofakes3 would otherwise
		// answer 501 NotImplemented.
		rangeRequest = nil
	}
	var rnge *gofakes3.ObjectRange
	if rangeRequest != nil && rangeRequest.FromEnd && size > 0 && rangeRequest.End > size {
		// A suffix range longer than the object selects the whole
		// representation (RFC 7233 2.1: "if the selected representation is
		// shorter than the specified suffix-length, the entire representation is
		// used"). gofakes3's ObjectRangeRequest.Range derives a negative start
		// for that case and reports ErrInvalidRange, which gofakes3 then answers
		// as 416 InvalidRange, so a tail read bigger than the stored file failed
		// where S3 returns the file.
		rnge = &gofakes3.ObjectRange{Start: 0, Length: size}
	} else {
		rnge, err = rangeRequest.Range(size)
		if err != nil {
			return nil, err
		}
	}
	// If-Range names the validator the client already holds. When it no longer
	// describes this object, S3 ignores Range and answers with the whole body;
	// honouring the range would make a resuming client append new bytes to a
	// file whose beginning has changed and report a corrupt download as success.
	// The validator If-Range names can be the ETag the gateway itself issued, so
	// it has to be read before the comparison rather than after the response is
	// built.
	validator := ""
	if stored, ok := b.loadObjectMetadata(ctx, fp); ok {
		// Read without consuming: the same cached map is merged into the response
		// metadata below, and a deletion here would erase the ETag for everyone.
		validator = storedETag(stored)
	}
	if ifRange := ifRangeFromContext(ctx); ifRange != "" &&
		!ifRangeMatchesValidator(ifRange, validator, node.ModTime()) {
		rnge = nil
	}

	rrf, err := stream.GetRangeReaderFromLink(size, link)
	if err != nil {
		return nil, fmt.Errorf("the remote storage driver need to be enhanced to support s3")
	}

	var rd io.ReadCloser
	if rnge != nil {
		rd, err = rrf.RangeRead(ctx, http_range.Range(*rnge))
	} else {
		rd, err = rrf.RangeRead(ctx, http_range.Range{Length: -1})
	}
	if err != nil {
		return nil, err
	}

	meta := map[string]string{
		"Last-Modified":       node.ModTime().UTC().Format(timeFormat),
		"Content-Disposition": utils.GenerateContentDisposition(file.GetName()),
		"Content-Type":        utils.GetMimeType(fp),
	}

	if stored, ok := b.loadObjectMetadata(ctx, fp); ok {
		for k, v := range stored {
			meta[k] = v
		}
	}
	closers := utils.NewClosers(rd, link)

	return &gofakes3.Object{
		// Name: gofakes3.URLEncode(objectName),
		Name: objectName,
		// Hash:     "",
		ETag:     takeStoredETag(meta),
		Metadata: meta,
		Size:     size,
		Range:    rnge,
		Contents: utils.ReadCloser{Reader: rd, Closer: &closers},
	}, nil
}

// TouchObject creates or updates meta on specified object.
func (b *s3Backend) TouchObject(ctx context.Context, fp string, meta map[string]string) (result gofakes3.PutObjectResult, err error) {
	//TODO: implement
	return result, gofakes3.ErrNotImplemented
}

// PutObject creates or overwrites the object with the given name.
func (b *s3Backend) PutObject(
	ctx context.Context, bucketName, objectName string,
	meta map[string]string,
	input io.Reader, size int64,
) (result gofakes3.PutObjectResult, err error) {
	return result, b.putStream(ctx, bucketName, objectName, meta, input, size)
}

// parentObjectConflict is the error for a key that cannot be stored because an
// ancestor of it is an object. S3 has no directories, so a real bucket would
// hold both keys at once; a POSIX-backed bucket cannot, and saying so is the
// only honest answer.
func parentObjectConflict(key string) error {
	return gofakes3.ErrorMessage(gofakes3.ErrInvalidArgument,
		fmt.Sprintf("the parent path of %q is an object, not a directory: nothing can be stored below it", key))
}

// findParentObject reports the first ancestor of objectName that exists as an
// object rather than a directory.
func findParentObject(ctx context.Context, bucketPath, objectName string) (string, bool) {
	parts := strings.Split(strings.Trim(objectName, "/"), "/")
	if len(parts) < 2 {
		return "", false
	}
	prefix := ""
	for _, part := range parts[:len(parts)-1] {
		if prefix == "" {
			prefix = part
		} else {
			prefix += "/" + part
		}
		fp, err := objectFSPath(bucketPath, prefix)
		if err != nil {
			return "", false
		}
		fmeta, _ := op.GetNearestMeta(fp)
		node, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), fp, &fs.GetArgs{})
		if err != nil {
			continue
		}
		if !node.IsDir() {
			return prefix, true
		}
	}
	return "", false
}

// putPreconditions carries the conditional headers of a PUT into putStream.
// The check belongs inside the per-key lock and before the upload body is
// consumed: a middleware-only If-None-Match check would race with another PUT
// and a failed condition would still have replaced the old object.
type putPreconditions struct {
	ifMatch     string
	ifNoneMatch string
}

type putPreconditionsContextKey struct{}

func withPutPreconditions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
			ifNoneMatch := strings.TrimSpace(r.Header.Get("If-None-Match"))
			if ifMatch != "" || ifNoneMatch != "" {
				r = r.WithContext(context.WithValue(r.Context(), putPreconditionsContextKey{},
					putPreconditions{ifMatch: ifMatch, ifNoneMatch: ifNoneMatch}))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// checkPutPreconditions compares the conditional headers with the object as it
// exists under the key lock. A missing object only satisfies If-None-Match;
// If-Match fails its precondition when there is no representation to match.
func (b *s3Backend) checkPutPreconditions(ctx context.Context, fp, objectName string) error {
	conditions, _ := ctx.Value(putPreconditionsContextKey{}).(putPreconditions)
	if conditions.ifMatch == "" && conditions.ifNoneMatch == "" {
		return nil
	}
	fmeta, _ := op.GetNearestMeta(fp)
	node, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), fp, &fs.GetArgs{})
	exists := err == nil && !node.IsDir()
	etag := ""
	if exists {
		if headers, ok := b.loadObjectMetadata(ctx, fp); ok {
			etag = storedETag(headers)
		}
	}
	if conditions.ifMatch != "" {
		if !exists {
			return gofakes3.KeyNotFound(objectName)
		}
		if !etagMatches(conditions.ifMatch, etag) {
			return gofakes3.ErrPreconditionFailed
		}
	}
	if conditions.ifNoneMatch != "" && exists && etagMatches(conditions.ifNoneMatch, etag) {
		return gofakes3.ErrPreconditionFailed
	}
	return nil
}

// putStream stores the given object into the underlying storage. It is shared
// by PutObject and the multipart-upload Complete step so both paths apply the
// same directory creation, metadata and ignore rules.
// s3RenameSupported reports whether the storage behind p can rename an object,
// which is what a staged upload needs to publish the new content in one step.
// Tests replace this hook.
var s3RenameSupported = func(ctx context.Context, p string) bool {
	storage, _, err := op.GetStorageAndActualPath(p)
	if err != nil {
		return false
	}
	if _, ok := storage.(driver.RenameResult); ok {
		return true
	}
	_, ok := storage.(driver.Rename)
	return ok
}

func (b *s3Backend) putStream(
	ctx context.Context, bucketName, objectName string,
	meta map[string]string,
	input io.Reader, size int64,
) error {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return err
	}
	bucketPath := bucket.Path

	isDir := strings.HasSuffix(objectName, "/")
	log.Debugf("isDir: %v", isDir)
	if isDir && (size > 0 || decodedContentLength(ctx) > 0) {
		// A key whose name ends in "/" is a directory here, and a directory
		// has no content: the body used to be dropped while the write still
		// answered 200, so a client that stored bytes under such a key lost
		// them silently. The check runs before anything is created, so a
		// rejected write leaves no empty directory behind (a listing renders
		// those as phantom keys).
		return gofakes3.ErrorMessage(gofakes3.ErrInvalidArgument,
			"a key ending in \"/\" cannot hold content")
	}

	fp, err := objectFSPath(bucketPath, objectName)
	if err != nil {
		return asS3KeyError(err)
	}
	// Content and metadata of one key must change together (see key_lock.go).
	// The lock is taken on the resolved path: two keys that differ only by
	// redundant slashes ("a//b", "a/b") name the same file, and locking the raw
	// names let them write it at the same time.
	unlock := b.keyLocks.lock(fp)
	defer unlock()
	if err := b.checkPutPreconditions(ctx, fp, objectName); err != nil {
		return err
	}
	log.Debugf("fp: %s, bucketPath: %s, objectName: %s", fp, bucketPath, objectName)

	var reqPath string
	if isDir {
		reqPath = fp + "/"
	} else {
		reqPath = path.Dir(fp)
	}
	log.Debugf("reqPath: %s", reqPath)
	fmeta, _ := op.GetNearestMeta(fp)
	// A key cannot become an object while it still names a folder holding
	// objects: the staged upload is renamed over the key, and that rename fails
	// with a bare 500 for a request any client can make. S3 keys are flat, so
	// "a" and "a/b" coexist there; this tree cannot represent that, and saying
	// so is better than an InternalError.
	{
		probeCtx := context.WithValue(ctx, conf.MetaKey, fmeta)
		if node, probeErr := fs.Get(probeCtx, fp, &fs.GetArgs{}); probeErr == nil && node.IsDir() {
			// An empty folder is what deleting its last child leaves behind; it
			// holds nothing, so the key may take its place.
			children, listErr := fs.List(probeCtx, fp, &fs.ListArgs{NoLog: true})
			emptied := false
			if listErr == nil && len(children) == 0 {
				emptied = fs.Remove(ctx, fp) == nil
			}
			if !emptied {
				return gofakes3.ErrorMessage(gofakes3.ErrInvalidArgument,
					"the key names a folder that still holds objects; delete them first")
			}
		}
	}
	ctx = context.WithValue(ctx, conf.MetaKey, fmeta)

	parentNode, err := fs.Get(ctx, reqPath, &fs.GetArgs{})
	madeDir := false
	if err == nil && !isDir && !parentNode.IsDir() {
		return parentObjectConflict(objectName)
	}
	// A storage driver answers "not a directory" when an ancestor of the key is
	// an object. That is a conflict with what is stored, not a missing path, so
	// it must not be reported as NoSuchKey: clients retry that forever.
	if err != nil && !errs.IsObjectNotFound(err) {
		if conflict, ok := findParentObject(ctx, bucketPath, objectName); ok {
			return parentObjectConflict(conflict + "/")
		}
	}
	if err != nil {
		if errs.IsObjectNotFound(err) && strings.Contains(objectName, "/") {
			log.Debugf("reqPath: %s not found and objectName contains /, need to makeDir", reqPath)
			err = fs.MakeDir(ctx, reqPath)
			if err != nil {
				return errors.WithMessagef(err, "failed to makeDir, reqPath: %s", reqPath)
			}
			madeDir = true
		} else {
			return gofakes3.KeyNotFound(objectName)
		}
	}

	if isDir {
		// The marker itself is allowed: clients create it on purpose, and a
		// directory has no content to store.
		return nil
	}

	var ti time.Time

	if val, ok := meta["X-Amz-Meta-Mtime"]; ok {
		ti, _ = swift.FloatStringToTime(val)
	}

	if val, ok := meta["mtime"]; ok {
		ti, _ = swift.FloatStringToTime(val)
	}

	// If Modified is not set, use current time
	if ti.IsZero() {
		ti = time.Now()
	}

	// The driver streams straight into the final key, so any failure after the
	// first byte either published a partial object or destroyed the good one it
	// replaced: a client that disconnects, a chunk frame that does not decode and
	// a body that fails its declared hash are all "error, but bytes are already
	// there". The upload therefore lands in a temporary sibling and is renamed
	// over the key only once length and hash checked out.
	// Staging a sibling and renaming it over the key is the only way to make
	// the replacement atomic, but it needs a driver that can rename. Mounts
	// without rename support (many cloud drivers) streamed straight to the key
	// before this and still have to: failing them with "not implemented" would
	// make every upload on those storages impossible.
	staged := s3RenameSupported(ctx, reqPath)
	commitName := path.Base(fp)
	if staged {
		commitName = s3TempKeyName()
	}
	commitPath := path.Join(path.Dir(fp), commitName)
	obj := model.Object{
		Name:     commitName,
		Size:     size,
		Modified: ti,
		Ctime:    time.Now(),
	}
	// Check if system file should be ignored
	if setting.GetBool(conf.IgnoreSystemFiles) && utils.IsSystemFile(path.Base(fp)) {
		return errs.IgnoredSystemFile
	}
	// The bytes that reach the driver are counted and hashed here, not in a
	// request wrapper: gofakes3 hands the storage a LimitReader sized from the
	// declared length, so a wrapper that compares at EOF may never see one. A
	// request that declares a hash, or a length, is only accepted when what was
	// written matches.
	declared := declaredPayloadHash(ctx)
	checksumAlgorithm, declaredChecksum := requestedChecksum(meta)
	checksumSum := newChecksumHash(checksumAlgorithm)
	var written int64
	var declaredSum hash.Hash
	// The MD5 feeds the ETag of every later GET, HEAD, listing entry and copy: a
	// gateway that answers no ETag breaks conditional writes, cache revalidation
	// and the change detection of every syncing client.
	contentMD5 := md5.New()
	writers := []io.Writer{contentMD5, &countingWriter{n: &written}}
	if declared != "" {
		declaredSum = sha256.New()
		writers = append(writers, declaredSum)
	}
	if checksumSum != nil {
		writers = append(writers, checksumSum)
	}
	bodyErr := &bodyErrorReader{inner: input}
	body := io.TeeReader(bodyErr, io.MultiWriter(writers...))

	stream := &stream.FileStream{
		Obj:      &obj,
		Reader:   body,
		Mimetype: meta["Content-Type"],
	}
	if stream.Mimetype == "" {
		stream.Mimetype = "application/octet-stream"
	}

	err = fs.PutDirectly(ctx, reqPath, stream)
	if err != nil && bodyErr.err != nil && !errors.Is(bodyErr.err, errPayloadHashMismatch) {
		// The write failed because the request body broke, not because the
		// storage did: a chunk frame that does not decode, a body that ends in
		// the middle of a frame, a connection that died mid-PUT. Left as the raw
		// error gofakes3 would answer 500 InternalError, and a retrying SDK would
		// resend a body that can never be accepted; S3 answers 400 for it.
		//
		// Two body errors keep their own answer: a declared-payload-hash
		// disagreement (say 400 BadDigest below) and anything already carrying an
		// S3 code, such as gofakes3's Content-MD5 verdict - those are classified,
		// and only the unclassified ones need a code assigned here.
		if _, classified := asS3ErrorCode(bodyErr.err); !classified {
			err = gofakes3.ErrIncompleteBody
		}
	}
	if err == nil && size >= 0 && written != size {
		// The client promised size bytes and delivered another number of them.
		// Uploading a truncated body used to answer 200 with an object shorter
		// than its own declared length, which no client-side checksum catches
		// because the ETag is computed from what actually arrived.
		err = gofakes3.ErrIncompleteBody
	}
	if err == nil {
		// A framed body can decode to fewer bytes than the client announced; the
		// truncation would otherwise be stored as if it were the whole object.
		if want := decodedContentLength(ctx); want > 0 && written != want {
			err = gofakes3.ErrIncompleteBody
		}
	}
	if err == nil {
		// The other direction: a framed body can carry more bytes than it
		// announced. gofakes3 caps its decoder at the announced length, so the
		// surplus never reached this function and "written != want" could not
		// see it - the truncated prefix was stored and answered 200, with an
		// ETag computed from the bytes that survived, so no client-side check
		// could notice. S3 refuses the request; nothing is published here.
		if watcher := chunkWatcherFromContext(ctx); watcher != nil {
			// Draining to the end of the framed body also reaches the trailer,
			// where a streaming upload carries the checksum it asks the gateway
			// to verify.
			switch {
			case watcher.overrunAfterDecodedBody():
				err = gofakes3.ErrIncompleteBody
			case watcher.trailerChecksumMismatch():
				// x-amz-trailer named a checksum and the body does not match it;
				// storing the body anyway made the trailer a decoration.
				err = gofakes3.ErrBadDigest
			}
		}
	}
	if err == nil && declaredSum != nil && hex.EncodeToString(declaredSum.Sum(nil)) != declared {
		err = errPayloadHashMismatch
	}
	if err == nil && checksumSum != nil && !checksumMatches(checksumAlgorithm, checksumSum.Sum(nil), declaredChecksum) {
		// The client asked the gateway to check its own integrity value; storing
		// the body anyway made the header a decoration.
		err = gofakes3.ErrBadDigest
	}
	if err != nil {
		err = asS3WriteError(err)
		// Nothing was published: only the temporary holds bytes, and it goes.
		// A staged upload only holds bytes in the temporary, so it always goes.
		// A direct one wrote into the key itself: its bytes are gone either way,
		// and leaving the partial content behind would publish an object that no
		// client asked for, so it is removed only once something was written.
		if staged || written > 0 {
			if rmErr := fs.Remove(ctx, commitPath); rmErr != nil {
				log.Debugf("failed to remove %s of a rejected PUT: %v", commitPath, rmErr)
			}
		}
		if madeDir {
			// Leave nothing behind: the listing renders every empty directory as
			// a phantom object, so a failed PUT would otherwise add a key that
			// cannot be read.
			// Only when it is still empty: the driver removes directories
			// recursively, so an unconditional call deletes objects another
			// request stored under the same prefix in the meantime.
			if children, listErr := fs.List(ctx, reqPath, &fs.ListArgs{NoLog: true}); listErr == nil && len(children) == 0 {
				if rmErr := fs.Remove(ctx, reqPath); rmErr != nil {
					log.Debugf("failed to remove the directory created for a failed PUT %s: %v", reqPath, rmErr)
				}
			}
		}
		return err
	}

	if b.putPausedAfterContent != nil {
		b.putPausedAfterContent()
	}
	if meta == nil {
		meta = map[string]string{}
	}
	if watcher := chunkWatcherFromContext(ctx); watcher != nil {
		// A framed body carries its checksum in the trailer rather than in a
		// header. Once the trailer has been read and the body matched it, the
		// value belongs to the object: S3 stores it and serves it on later
		// GET/HEAD calls, and the answer to this PUT echoes it. Verifying it and
		// dropping it left the client unable to confirm what it asked to check.
		if algorithm, value, ok := watcher.verifiedTrailerChecksum(); ok {
			meta[http.CanonicalHeaderKey(checksumHeaderPrefix+algorithm)] = value
			recordTrailerChecksum(ctx, algorithm, value)
			if checksumAlgorithm == "" {
				// Keep the value dropUnverifiableChecksums is about to prune
				// against: a header-declared checksum of another algorithm, or
				// none at all, must not remove the one the trailer verified.
				checksumAlgorithm = algorithm
			}
		}
	}
	meta[etagMetadataKey] = hex.EncodeToString(contentMD5.Sum(nil))
	dropUnverifiableChecksums(meta, checksumAlgorithm)
	// Commit: the whole upload checked out. A staged upload replaces the key in
	// one step; a direct one is already in place under its final name.
	if staged {
		if err := fs.Rename(ctx, commitPath, path.Base(fp)); err != nil {
			if rmErr := fs.Remove(ctx, commitPath); rmErr != nil {
				log.Debugf("failed to remove the temporary %s after a failed commit: %v", commitPath, rmErr)
			}
			return asS3WriteError(err)
		}
	}
	b.storeObjectMetadata(ctx, fp, meta)

	return nil
}

// countingWriter counts the bytes that pass through it.
type countingWriter struct{ n *int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	*w.n += int64(len(p))
	return len(p), nil
}

// maxMultiDeleteKeys is the S3 limit on one DeleteObjects document. The XML is
// accepted above it by the generic parser, so the gateway has to refuse it:
// silently deleting 5000 keys in a request its author believes was rejected is
// a data-loss surprise, and the 1 MiB body cap alone allows far more keys than
// the API documents.
const maxMultiDeleteKeys = 1000

// DeleteMulti deletes multiple objects in a single request.
func (b *s3Backend) DeleteMulti(ctx context.Context, bucketName string, objects ...string) (result gofakes3.MultiDeleteResult, rerr error) {
	if len(objects) > maxMultiDeleteKeys {
		return result, gofakes3.ErrorMessage(gofakes3.ErrMalformedXML,
			"You have attempted to delete more objects than allowed.")
	}
	for _, object := range objects {
		if err := b.deleteObject(ctx, bucketName, object); err != nil {
			log.Errorf("delete object failed: %v", err)
			result.Error = append(result.Error, gofakes3.ErrorResult{
				Code:    gofakes3.ErrInternal,
				Message: gofakes3.ErrInternal.Message(),
				Key:     object,
			})
		} else {
			result.Deleted = append(result.Deleted, gofakes3.ObjectID{
				Key: object,
			})
		}
	}

	return result, nil
}

// DeleteObject deletes the object with the given name.
func (b *s3Backend) DeleteObject(ctx context.Context, bucketName, objectName string) (result gofakes3.ObjectDeleteResult, rerr error) {
	return result, b.deleteObject(ctx, bucketName, objectName)
}

// deleteObject deletes the object from the filesystem.
func (b *s3Backend) deleteObject(ctx context.Context, bucketName, objectName string) error {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return err
	}
	bucketPath := bucket.Path

	// The sentinel key that listings invent for an empty folder
	// ("<dir>/<emptyObjectName>") is the only handle a client has on that
	// folder, so deleting it has to remove the folder. It never existed on
	// disk: a recursive delete would otherwise enumerate a key that can never
	// be removed and never converge.
	if dirName, ok := emptyFolderSentinelDir(objectName); ok {
		dirPath, dirErr := objectFSPath(bucketPath, dirName)
		if dirErr != nil {
			return nil
		}
		unlockDir := b.keyLocks.lock(dirPath)
		defer unlockDir()
		dirMeta, _ := op.GetNearestMeta(dirPath)
		return b.deleteEmptyDir(ctx, dirPath, dirMeta)
	}

	fp, err := objectFSPath(bucketPath, objectName)
	if err != nil {
		return asS3KeyError(err)
	}
	unlock := b.keyLocks.lock(fp)
	defer unlock()
	fmeta, _ := op.GetNearestMeta(fp)
	// S3 does not report an error when attemping to delete a key that does not exist, so
	// we need to skip IsNotExist errors.
	node, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), fp, &fs.GetArgs{})
	if err != nil && !errs.IsObjectNotFound(err) {
		if asS3KeyError(err) != nil {
			// The key cannot name an object here (too long, blank or dot
			// segment). S3 answers a delete of a key that cannot exist with
			// success, and a 500 for it would make sync tools retry forever.
			return nil
		}
		return err
	}
	if err == nil && node.IsDir() {
		// A key that names a directory is not an object: reads answer NoSuchKey
		// for it, so a delete must not walk in and remove the whole subtree.
		// The one shape that has to act is the explicit "dir/" marker a client
		// created with PUT: it stands for the folder, so deleting it removes the
		// folder when that folder is empty, and does nothing when it is not.
		if isDirMarkerRequest(ctx) || strings.HasSuffix(objectName, "/") {
			return b.deleteEmptyDir(ctx, fp, fmeta)
		}
		// Reporting success matches S3's "deleting a missing key is not an
		// error" contract without touching anything.
		return nil
	}
	if isDirMarkerRequest(ctx) || strings.HasSuffix(objectName, "/") {
		// Deleting "prefix/" must not remove the file "prefix": S3 keeps those
		// as two keys and only the marker was addressed. Nothing was ever stored
		// under the marker key, so this succeeds without touching the file.
		return nil
	}

	if err := fs.Remove(ctx, fp); err != nil && !errs.IsObjectNotFound(err) {
		// A 204 has to mean the object is gone. Losing the error here left the
		// object readable while its metadata row was already purged.
		return err
	}
	// Drop the row before the cache entry: a HEAD that misses the cache would
	// otherwise read the row back and describe an object that no longer exists.
	b.deleteStoredMetadata(ctx, fp)
	b.meta.Delete(fp)
	// A read recorded just before the delete must not recreate the row later.
	b.access.forget(fp)
	access.Forget(fp)
	return nil
}

// CreateBucket creates a new bucket.
func (b *s3Backend) CreateBucket(ctx context.Context, name string) error {
	return gofakes3.ErrNotImplemented
}

// DeleteBucket deletes the bucket with the given name.
func (b *s3Backend) DeleteBucket(ctx context.Context, name string) error {
	return gofakes3.ErrNotImplemented
}

// BucketExists checks if the bucket exists.
func (b *s3Backend) BucketExists(ctx context.Context, name string) (exists bool, err error) {
	buckets, err := getAndParseBuckets()
	if err != nil {
		return false, err
	}
	for _, b := range buckets {
		if b.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// CopyObject copy specified object from srcKey to dstKey.
func (b *s3Backend) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string, meta map[string]string) (result gofakes3.CopyObjectResult, err error) {
	srcB, err := getBucketByName(srcBucket)
	if err != nil {
		return result, err
	}
	srcBucketPath := srcB.Path

	srcFp, err := objectFSPath(srcBucketPath, srcKey)
	if err != nil {
		return result, gofakes3.KeyNotFound(srcKey)
	}
	if strings.HasSuffix(srcKey, "/") {
		// "key/" and "key" are different keys in S3. Only the former is a
		// directory marker here, so a copy that names the marker must not read
		// the bytes of the same-named file.
		srcMeta, _ := op.GetNearestMeta(srcFp)
		if node, gerr := fs.Get(context.WithValue(ctx, conf.MetaKey, srcMeta), srcFp, &fs.GetArgs{}); gerr == nil && !node.IsDir() {
			return result, gofakes3.KeyNotFound(srcKey)
		}
	}
	if srcBucket == dstBucket && srcKey == dstKey {
		// A self-copy is how S3 replaces an object's metadata in place: the
		// content is not rewritten, and the metadata described by the request
		// replaces what the object had.
		return b.replaceObjectMetadata(ctx, srcFp, meta)
	}
	fmeta, _ := op.GetNearestMeta(srcFp)
	srcNode, _ := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), srcFp, &fs.GetArgs{})

	c, err := b.GetObject(ctx, srcBucket, srcKey, nil)
	if err != nil {
		return
	}
	defer func() {
		_ = c.Contents.Close()
	}()

	// The source node is nil whenever GetObject answered a key that no object
	// backs: the "<dir>/ThisIsAnEmptyFolderInTheS3Bucket" key a listing invents
	// for an empty folder, and the "dir/" marker a client created with PUT.
	// Dereferencing it panicked the handler, so a signed copy of that key reset
	// the connection instead of copying zero bytes; the modification time then
	// comes from the Last-Modified the object itself reports.
	srcModified := time.Time{}
	if srcNode != nil {
		srcModified = srcNode.ModTime()
	} else if stamp := c.Metadata["Last-Modified"]; stamp != "" {
		if parsed, perr := http.ParseTime(stamp); perr == nil {
			srcModified = parsed
		}
	}

	// REPLACE means the request alone describes the destination, so inheriting
	// the source metadata would answer a request that asked to replace it with
	// the values it meant to discard. COPY, the default, does inherit.
	replace := isMetadataReplace(meta)
	deleteMetadataDirective(meta)
	if !replace {
		for k, v := range c.Metadata {
			if _, found := meta[k]; !found && k != "X-Amz-Acl" {
				meta[k] = v
			}
		}
	}
	if _, ok := meta["mtime"]; !ok && !srcModified.IsZero() {
		meta["mtime"] = swift.TimeToFloatString(srcModified)
	}

	dstB, err := getBucketByName(dstBucket)
	if err != nil {
		return
	}

	_, err = b.PutObject(ctx, dstBucket, dstKey, meta, c.Contents, c.Size)
	if err != nil {
		return
	}
	// CopyObject reuploads the content, and c.Metadata only carries what this
	// process still remembers, so the recorded copy is applied afterwards. A
	// REPLACE copy must not take the source row: it holds the very metadata the
	// request asked to drop.
	dstETag := ""
	dstFp, dstErr := objectFSPath(dstB.Path, dstKey)
	if dstErr == nil {
		if !replace {
			b.copyStoredMetadata(ctx, srcFp, dstFp)
		}
		// The copy streamed the source bytes, so the ETag of the destination was
		// computed during that upload; answering an empty one made clients treat
		// a successful copy as an object they cannot validate.
		if stored, ok := b.loadObjectMetadata(ctx, dstFp); ok {
			dstETag = storedETag(stored)
		}
	}

	if srcModified.IsZero() {
		srcModified = time.Now().UTC()
	}
	return gofakes3.CopyObjectResult{
		ETag:         dstETag,
		LastModified: gofakes3.NewContentTime(srcModified),
	}, nil
}

// emptyFolderSentinelDir reports the folder that a listing-only key stands for.
// Listings invent "<dir>/ThisIsAnEmptyFolderInTheS3Bucket" so that a client can
// see a folder holding no object; that key exists nowhere on disk.
func emptyFolderSentinelDir(objectName string) (string, bool) {
	if !strings.HasSuffix(objectName, "/"+emptyObjectName) {
		return "", false
	}
	dir := strings.TrimSuffix(objectName, "/"+emptyObjectName)
	if dir == "" {
		return "", false
	}
	return dir, true
}

// deleteEmptyDir removes a folder that a delete names and that holds nothing.
// A folder with content is left alone: S3 has no directory semantics, so a
// delete of a prefix must never behave like a recursive delete.
func (b *s3Backend) deleteEmptyDir(ctx context.Context, fp string, fmeta *model.Meta) error {
	cctx := context.WithValue(ctx, conf.MetaKey, fmeta)
	children, err := fs.List(cctx, fp, &fs.ListArgs{NoLog: true})
	if err != nil {
		if errs.IsObjectNotFound(err) {
			return nil
		}
		return err
	}
	if len(children) > 0 {
		return nil
	}
	if err := fs.Remove(cctx, fp); err != nil && !errs.IsObjectNotFound(err) {
		return err
	}
	// A read recorded just before the delete must not recreate a row later.
	b.access.forget(fp)
	access.Forget(fp)
	return nil
}
