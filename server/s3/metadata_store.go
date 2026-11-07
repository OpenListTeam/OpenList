package s3

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OpenListTeam/gofakes3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	log "github.com/sirupsen/logrus"
)

// User metadata (x-amz-meta-*) is only ever known to the S3 server: most
// storage drivers have nowhere to keep it, and extended attributes exist for
// files on this machine only. It is therefore persisted in the database, keyed
// on the object path, so that it outlives the process that received the upload.
//
// Every function here is best effort. The S3 server must keep serving objects
// when the database is unavailable, so a failure only degrades metadata back to
// the previous in-memory-only behaviour instead of failing the request.

// s3MetadataRetries mirrors the dead-property handling in server/webdav: SQLite
// reports "database is locked" while another writer holds the lock, which is
// transient and worth a short retry.
const s3MetadataRetries = 3

// userMetadataPrefix is the request header prefix S3 uses for user metadata.
// gofakes3 keeps the header names it received, so comparisons ignore case.
const userMetadataPrefix = "x-amz-meta-"

// takeStoredETag extracts and removes the multipart composite ETag if present.
func takeStoredETag(meta map[string]string) string {
	for k, v := range meta {
		if strings.EqualFold(k, etagMetadataKey) {
			delete(meta, k)
			v = strings.TrimSpace(v)
			if !strings.HasPrefix(v, `"`) {
				v = `"` + v + `"`
			}
			return v
		}
	}
	return ""
}

// objectHeaders names the request headers that describe the object itself and
// are therefore worth keeping: a client that sets one of them expects it back
// on every later GET/HEAD. Everything else in the request is request-scoped
// (see storableMetadata).
var objectHeaders = map[string]bool{
	"content-type":        true,
	"content-disposition": true,
	"content-encoding":    true,
	"content-language":    true,
	"cache-control":       true,
	"expires":             true,
	// A redirect location is a property of the object, not of the upload, and
	// clients read it back with HEAD/GET.
	"x-amz-website-redirect-location": true,
}

// isWholeObjectChecksumHeader reports whether a stored header carries a checksum
// of the complete object (`x-amz-checksum-crc32`, `-crc32c`, `-sha1`, `-sha256`,
// `-crc64nvme`, ...) or names the algorithm that produced it. These values are
// valid only on responses that carry the complete object.
func isWholeObjectChecksumHeader(name string) bool {
	h := strings.ToLower(name)
	return strings.HasPrefix(h, "x-amz-checksum-") || h == "x-amz-sdk-checksum-algorithm"
}

// hasUserMetadata reports whether headers carry anything the storage driver
// cannot reproduce by itself.
// storableMetadata filters a header set down to what describes the object
// rather than the upload that created it.
//
// gofakes3 hands every X-Amz-* header of the request to the backend as object
// metadata, and the values are written back as response headers on every later
// GET/HEAD. Storing them unfiltered replayed the request that stored the object:
// X-Amz-Copy-Source revealed which other bucket and key an object was copied
// from, X-Amz-Security-Token published the temporary credentials of the writer
// to every reader, X-Amz-Date/X-Amz-Content-Sha256 described one past request,
// and a stored Content-Length could contradict the object's real size. Only user
// metadata and the headers that are part of the object itself are kept.
func storableMetadata(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		h := strings.ToLower(key)
		if strings.HasPrefix(h, userMetadataPrefix) {
			out[userMetadataHeaderKey(key)] = value
			continue
		}
		if h == "content-encoding" {
			// "aws-chunked" describes how this request framed the body, not the
			// object. AWS strips it; replaying it made a GET advertise an encoding
			// the stored bytes do not have.
			if value := withoutAWSChunked(value); value != "" {
				out[key] = value
			}
			continue
		}
		// Checksums of the complete object are part of it, and are already
		// filtered out of ranged responses (see isWholeObjectChecksumHeader,
		// which only exists in the ETag variants) - the test is inlined here so
		// this file does not depend on the zerofs patches.
		if strings.EqualFold(h, strings.ToLower(etagMetadataKey)) || objectHeaders[h] || strings.HasPrefix(h, "x-amz-checksum-") || h == "x-amz-sdk-checksum-algorithm" {
			out[key] = value
		}
	}
	return out
}

// userMetadataHeaderKey lowercases the suffix of a user metadata header: Go
// canonicalises header names, so a client that stored "team" got "Team" back.
func userMetadataHeaderKey(key string) string {
	if len(key) <= len(userMetadataPrefix) {
		return key
	}
	return userMetadataPrefix + strings.ToLower(key[len(userMetadataPrefix):])
}

// withoutAWSChunked drops the transfer framing from a Content-Encoding value.
func withoutAWSChunked(value string) string {
	kept := make([]string, 0, 2)
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" || strings.EqualFold(part, "aws-chunked") {
			continue
		}
		kept = append(kept, part)
	}
	return strings.Join(kept, ", ")
}

func hasUserMetadata(headers map[string]string) bool {
	for key := range headers {
		if strings.HasPrefix(strings.ToLower(key), userMetadataPrefix) {
			return true
		}
	}
	return false
}

// userMetadataOf extracts the user metadata out of a header set.
func userMetadataOf(headers map[string]string) map[string]string {
	user := map[string]string{}
	for key, value := range headers {
		if strings.HasPrefix(strings.ToLower(key), userMetadataPrefix) {
			user[key] = value
		}
	}
	return user
}

// isMetadataReplace reports whether the request asked for the metadata of the
// destination to come from the request alone instead of from the source object.
func isMetadataReplace(headers map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(metadataDirective(headers)), "REPLACE")
}

// deleteMetadataDirective drops the request control header: it selects how a
// copy builds the metadata, but it is not metadata itself. Storing it would make
// every later GET and HEAD replay a header the client only sent with the copy.
func deleteMetadataDirective(headers map[string]string) {
	for key := range headers {
		if strings.EqualFold(key, "X-Amz-Metadata-Directive") {
			delete(headers, key)
		}
	}
}

func isDatabaseLocked(err error) bool {
	return err != nil && strings.Contains(err.Error(), "database is locked")
}

func withS3MetadataTransaction(ctx context.Context, fn func(*gorm.DB) error) error {
	database := db.GetDb()
	if database == nil {
		return errors.New("s3 metadata database is not initialized")
	}
	var err error
	for attempt := 0; attempt < s3MetadataRetries; attempt++ {
		err = database.WithContext(ctx).Transaction(fn)
		if !isDatabaseLocked(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	return err
}

// loadStoredMetadata returns the user metadata recorded for an object, if any.
func (b *s3Backend) loadStoredMetadata(ctx context.Context, name string) (map[string]string, bool) {
	row, ok := loadStoredRow(ctx, name)
	if !ok {
		return nil, false
	}
	headers := map[string]string{}
	if err := json.Unmarshal(row.Headers, &headers); err != nil || len(headers) == 0 {
		return nil, false
	}
	return headers, true
}

func (b *s3Backend) setStoredETag(ctx context.Context, name, etag string) {
	etag = strings.Trim(etag, "\"")
	if etag == "" {
		return
	}
	headers, ok := b.loadStoredMetadata(ctx, name)
	if !ok {
		headers = map[string]string{}
	} else {
		copied := make(map[string]string, len(headers)+1)
		for key, value := range headers {
			copied[key] = value
		}
		headers = copied
	}
	headers[etagMetadataKey] = etag
	b.storeStoredMetadata(ctx, name, headers)
	if value, ok := b.meta.Load(name); ok {
		if meta, isMeta := value.(objectMetadata); isMeta {
			meta.etag = `"` + etag + `"`
			if meta.headers == nil {
				meta.headers = map[string]string{}
			}
			meta.headers[etagMetadataKey] = etag
			b.meta.Store(name, meta)
		}
	}
}

// storeStoredMetadata records the user metadata of an object. An object that
// carries none owns no row in performance mode, which keeps the table empty for
// the vast majority of objects. In compat mode, standard object headers are
// also persisted.
func (b *s3Backend) storeStoredMetadataForBucket(ctx context.Context, bucketName, name string, headers map[string]string) {
	var clean map[string]string
	if isPerformanceMode(bucketName) && headers[etagMetadataKey] == "" {
		if !hasUserMetadata(headers) {
			b.clearStoredMetadata(ctx, name)
			return
		}
		clean = userMetadataOf(headers)
	} else {
		clean = storableMetadata(headers)
		if len(clean) == 0 {
			b.clearStoredMetadata(ctx, name)
			return
		}
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		return
	}
	err = withS3MetadataTransaction(ctx, func(tx *gorm.DB) error {
		// An upsert, not a read-modify-write: the row usually exists already
		// (the upload that stored the object created it), and updating it through
		// FirstOrCreate's Assign does not persist the new headers, which left a
		// metadata-only copy alive in the cache and reverted at the next restart.
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key_hash"}},
			DoUpdates: clause.AssignmentColumns([]string{"headers"}),
		}).Create(&model.S3ObjectMetadata{Path: name, Headers: encoded}).Error
	})
	if err != nil {
		log.Debugf("s3: store metadata for %q: %v", name, err)
	}
}

func (b *s3Backend) storeStoredMetadata(ctx context.Context, name string, headers map[string]string) {
	b.storeStoredMetadataForBucket(ctx, "", name, headers)
}

// clearStoredMetadata drops the user metadata of an object that was overwritten
// without any. The row itself survives when it also records the ETag of the
// object, so that recording one and storing headers stay independent of each
// other.
func (b *s3Backend) clearStoredMetadata(ctx context.Context, name string) {
	err := withS3MetadataTransaction(ctx, func(tx *gorm.DB) error {
		return tx.Model(&model.S3ObjectMetadata{}).
			Where("key_hash = ?", model.HashS3ObjectMetadataKey(name)).
			Update("headers", []byte("{}")).Error
	})
	if err != nil {
		log.Debugf("s3: clear metadata for %q: %v", name, err)
	}
}

// deleteStoredMetadata drops the metadata of a deleted object. Deleting a key
// that has no row is not an error.
func (b *s3Backend) deleteStoredMetadata(ctx context.Context, name string) {
	err := withS3MetadataTransaction(ctx, func(tx *gorm.DB) error {
		return tx.Where("key_hash = ?", model.HashS3ObjectMetadataKey(name)).
			Delete(&model.S3ObjectMetadata{}).Error
	})
	if err != nil {
		log.Debugf("s3: delete metadata for %q: %v", name, err)
	}
}

// storeObjectMetadata keeps the metadata of an upload in the in-memory cache and
// in the database, so that a HEAD answers from memory for the process that
// received the upload and from the database after a restart. The content hash is
// cached with it so that a later HEAD can tell whether the metadata still
// describes the content it was uploaded with.
func (b *s3Backend) storeObjectMetadataForBucket(ctx context.Context, bucketName, name string, headers map[string]string, hash []byte, etag string) {
	headers = storableMetadata(headers)
	// The object exists again: a delete that raced the batch in flight must not
	// drop the row written here (see accessTracker.present).
	b.access.present(name)
	b.cacheObjectMetadata(name, objectMetadata{headers: headers, hash: hash, etag: etag})
	b.storeStoredMetadataForBucket(ctx, bucketName, name, headers)
}

func (b *s3Backend) storeObjectMetadata(ctx context.Context, name string, headers map[string]string, hash []byte, etag string) {
	b.storeObjectMetadataForBucket(ctx, "", name, headers, hash, etag)
}

// metaCacheLimit bounds the in-memory metadata cache. It holds one entry per
// object this process has written or read metadata for, so without a bound it
// would grow with the number of objects in every bucket. The cache is only an
// optimisation — a miss falls back to the database and, for a hash miss, to
// recomputing the object hash — so dropping it whole is always safe.
const metaCacheLimit = 8192

// cacheObjectMetadata records one entry and drops the whole cache once it grows
// past metaCacheLimit. Dropping everything instead of evicting one key keeps the
// bookkeeping to a counter: the map is shared by concurrent requests and any
// per-key eviction order would need more state than the cache is worth.
func (b *s3Backend) cacheObjectMetadata(name string, metadata objectMetadata) {
	b.metaMu.Lock()
	defer b.metaMu.Unlock()

	b.meta.Store(name, metadata)
	b.metaSize++
	if b.metaSize <= metaCacheLimit {
		return
	}
	// Deletes happen from other goroutines too and do not decrement metaSize, so
	// the counter can only overstate the size; the cache is cleared a little
	// early in that case, which costs a database lookup and nothing else.
	b.meta.Range(func(key, _ any) bool {
		b.meta.Delete(key)
		return true
	})
	b.metaSize = 0
}

// objectMetadataOf returns the metadata of an object together with the ETag the
// cache already knows. The in-memory cache only answers while it still describes
// the current content; the database answers otherwise, which is what makes user
// metadata outlive the process. A database hit repopulates the cache, so only
// the first HEAD of an object after a restart pays for the query.
func (b *s3Backend) objectMetadataOf(ctx context.Context, name string, hash []byte) (map[string]string, string) {
	if headers, etag := b.loadMetadata(name, hash); etag != "" {
		if storedETag := takeStoredETag(headers); storedETag != "" {
			return headers, storedETag
		}
		return headers, etag
	}
	headers, ok := b.loadStoredMetadata(ctx, name)
	if !ok {
		if len(hash) > 0 {
			etag := fmt.Sprintf("%q", hex.EncodeToString(hash))
			b.cacheObjectMetadata(name, objectMetadata{headers: nil, hash: hash, etag: etag})
			return nil, etag
		}
		return nil, ""
	}
	// The ETag of the content the hash describes, in the same quoted form the
	// upload path caches and the HEAD reports. Returning nothing here used to
	// leave every cold-cache If-Range request compared against "", which never
	// matches a strong tag: the client then got the whole object instead of the
	// range, on exactly the clients that read block by block.
	etag := fmt.Sprintf("%q", hex.EncodeToString(hash))
	if storedETag := takeStoredETag(headers); storedETag != "" {
		etag = storedETag
	}
	b.cacheObjectMetadata(name, objectMetadata{headers: headers, hash: hash, etag: etag})
	return headers, etag
}

// copyStoredMetadata gives dst the user metadata recorded for src, which is
// what x-amz-metadata-directive: COPY asks for.
func (b *s3Backend) copyStoredMetadata(ctx context.Context, src, dst string) {
	headers, ok := b.loadStoredMetadata(ctx, src)
	if !ok {
		return
	}
	// The copy has just been uploaded, so only the database needs the metadata:
	// the in-memory entry of the destination was written by that upload and is
	// already correct for the content the copy happens to share with the source.
	b.storeStoredMetadata(ctx, dst, headers)
}

// replaceObjectMetadata applies meta to an existing object without rewriting
// its content, which is what a self-copy does in S3. Only the user metadata in
// the request takes effect: the rest of meta was derived from the request or
// the source object and would only overwrite equally good values.
func (b *s3Backend) replaceObjectMetadata(ctx context.Context, name string, meta map[string]string) (gofakes3.CopyObjectResult, error) {
	var result gofakes3.CopyObjectResult
	modTime := time.Now().UTC()
	if fmeta, _ := op.GetNearestMeta(name); fmeta != nil {
		if node, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), name, &fs.GetArgs{}); err == nil && node != nil {
			modTime = node.ModTime().UTC()
		}
	}
	result.LastModified = gofakes3.NewContentTime(modTime)

	// A request that carries user metadata describes the new metadata in full.
	// The same is true of an explicit REPLACE with none, which is the documented
	// way to strip an object's metadata without rewriting its content; a plain
	// self-copy asks for the metadata to be left alone.
	replacing := hasUserMetadata(meta) || strings.EqualFold(metadataDirective(meta), "REPLACE")
	if !replacing {
		if value, ok := b.meta.Load(name); ok {
			if cached, isCached := value.(objectMetadata); isCached {
				result.ETag = `"` + strings.Trim(cached.etag, `"`) + `"`
			}
		}
		return result, nil
	}
	replaced := userMetadataOf(meta)
	b.storeStoredMetadata(ctx, name, replaced)

	// The in-memory entry of the process that received the upload still carries
	// the previous metadata, so it is rewritten to match.
	if value, ok := b.meta.Load(name); ok {
		if cached, isCached := value.(objectMetadata); isCached {
			cleaned := map[string]string{}
			for key, value := range cached.headers {
				if !strings.HasPrefix(strings.ToLower(key), userMetadataPrefix) {
					cleaned[key] = value
				}
			}
			for key, value := range replaced {
				cleaned[key] = value
			}
			cached.headers = cleaned
			b.meta.Store(name, cached)
			result.ETag = `"` + strings.Trim(cached.etag, `"`) + `"`
		}
	}

	return result, nil
}

// metadataDirective returns the value of x-amz-metadata-directive however the
// request happened to spell the header.
func metadataDirective(meta map[string]string) string {
	for key, value := range meta {
		if strings.EqualFold(key, "x-amz-metadata-directive") {
			return value
		}
	}
	return ""
}
