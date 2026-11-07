package s3

import (
	"context"
	"encoding/json"
	"errors"
	"path"
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
// metadata, the headers that are part of the object itself and whole-object
// checksums are kept.
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
			// the stored bytes do not have, and only on the proxied path (a 302
			// direct link never carries it).
			if value := withoutAWSChunked(value); value != "" {
				out[key] = value
			}
			continue
		}
		if objectHeaders[h] || strings.HasPrefix(h, "x-amz-checksum-") || h == "x-amz-sdk-checksum-algorithm" ||
			h == strings.ToLower(etagMetadataKey) {
			out[key] = value
		}
	}
	return out
}

// userMetadataHeaderKey normalises a user metadata header to the shape AWS
// emits: "x-amz-meta-" plus a lowercase suffix. Go canonicalises incoming header
// names ("x-amz-meta-k" arrives as "X-Amz-Meta-K"), so passing them through
// unchanged made a GET answer "X-Amz-Meta-K" and clients that read back the key
// they sent (boto3: obj["Metadata"]["k"]) found nothing. Amazon documents that
// user metadata keys are stored in lowercase, so lowering them is the compatible
// choice.
func userMetadataHeaderKey(key string) string {
	if len(key) <= len(userMetadataPrefix) {
		return key
	}
	return userMetadataPrefix + strings.ToLower(key[len(userMetadataPrefix):])
}

// normalizeUserMetadataKeys returns the header set with every user metadata key
// in that shape. Stored rows written before the normalisation keep the
// canonicalised spelling, so they are fixed up on the way out.
func normalizeUserMetadataKeys(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		if strings.HasPrefix(strings.ToLower(key), userMetadataPrefix) {
			key = userMetadataHeaderKey(key)
		}
		out[key] = value
	}
	return out
}

// withoutAWSChunked drops the aws-chunked token from a Content-Encoding value.
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

// hasUserMetadata reports whether headers carry anything the storage driver
// cannot reproduce by itself.
func hasUserMetadata(headers map[string]string) bool {
	for key := range headers {
		if strings.HasPrefix(strings.ToLower(key), userMetadataPrefix) {
			return true
		}
	}
	return false
}

// takeStoredETag removes the ETag the gateway recorded at write time from a
// metadata set and returns it quoted, ready to be answered as a header. It must
// never be emitted as metadata: the client did not send it.
func takeStoredETag(meta map[string]string) string {
	for key, value := range meta {
		if strings.EqualFold(key, etagMetadataKey) {
			delete(meta, key)
			if value == "" {
				return ""
			}
			return "\"" + value + "\""
		}
	}
	return ""
}

// storedETag returns the quoted ETag recorded for a header set, without
// touching it.
func storedETag(headers map[string]string) string {
	for key, value := range headers {
		if strings.EqualFold(key, etagMetadataKey) && value != "" {
			return "\"" + value + "\""
		}
	}
	return ""
}

// setStoredETag records the ETag an object must answer. A multipart object gets
// a composite value that cannot be derived from the assembled bytes, so the one
// computed while streaming is replaced here.
func (b *s3Backend) setStoredETag(ctx context.Context, name, etag string) {
	etag = strings.Trim(etag, "\"")
	if etag == "" {
		return
	}
	headers, ok := b.loadObjectMetadata(ctx, name)
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
	b.storeObjectMetadata(ctx, name, headers)
}

// etagsForEntries answers the ETag of every object in one directory with a
// single query: a listing carries one ETag per key, and clients compare them to
// decide whether to download again.
func (b *s3Backend) etagsForEntries(ctx context.Context, dirFSPath string, entries []model.Obj) map[string]string {
	database := db.GetDb()
	if database == nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	byHash := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// The rows are keyed by the filesystem path of the object, while the
		// caller emits base names; both sides are mapped through byHash.
		full := path.Join(dirFSPath, entry.GetName())
		names = append(names, full)
		byHash[model.HashS3ObjectMetadataKey(full)] = entry.GetName()
	}
	if len(names) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(byHash))
	for hash := range byHash {
		hashes = append(hashes, hash)
	}
	var rows []model.S3ObjectMetadata
	if err := database.WithContext(ctx).Where("key_hash IN ?", hashes).Find(&rows).Error; err != nil {
		log.Debugf("s3: list etags: %v", err)
		return nil
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		headers := map[string]string{}
		if err := json.Unmarshal(row.Headers, &headers); err != nil {
			continue
		}
		etag := ""
		for key, value := range headers {
			if strings.EqualFold(key, etagMetadataKey) {
				etag = value
				break
			}
		}
		if etag == "" {
			continue
		}
		if base, ok := byHash[row.KeyHash]; ok {
			out[base] = "\"" + etag + "\""
		}
	}
	return out
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
	database := db.GetDb()
	if database == nil {
		return nil, false
	}
	var row model.S3ObjectMetadata
	err := database.WithContext(ctx).
		Where("key_hash = ?", model.HashS3ObjectMetadataKey(name)).
		Take(&row).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Debugf("s3: load metadata for %q: %v", name, err)
		}
		return nil, false
	}
	headers := map[string]string{}
	if err := json.Unmarshal(row.Headers, &headers); err != nil || len(headers) == 0 {
		return nil, false
	}
	return normalizeUserMetadataKeys(headers), true
}

// storeStoredMetadata records the user metadata of an object. An object that
// carries none owns no headers, which is what keeps the table empty for the vast
// majority of objects.
func (b *s3Backend) storeStoredMetadata(ctx context.Context, name string, headers map[string]string) {
	if len(headers) == 0 {
		b.clearStoredMetadata(ctx, name)
		return
	}
	encoded, err := json.Marshal(headers)
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

// clearStoredMetadata drops the user metadata of an object that no longer
// carries any, leaving the rest of the row alone.
//
// User metadata and the access record share one row (model.S3ObjectMetadata),
// but their lifetimes are opposite: headers belong to the upload, last_access to
// the read tracker. Deleting the row to say "this object has no user metadata"
// therefore erased the access record of a hot object the first time it was
// re-uploaded - a plain `aws s3 cp` that sets only Content-Type was enough - and
// the tracker's write throttle, which still believed the path had been written,
// then refused to recreate it for an hour. The object disappeared from the
// access listing while clients kept reading it, so tiering saw it as cold.
func (b *s3Backend) clearStoredMetadata(ctx context.Context, name string) {
	hash := model.HashS3ObjectMetadataKey(name)
	err := withS3MetadataTransaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Model(&model.S3ObjectMetadata{}).
			Where("key_hash = ?", hash).
			Update("headers", nil).Error; err != nil {
			return err
		}
		// A row that never had an access record either is dead weight; dropping
		// it keeps the table proportional to the objects that carry something.
		return tx.Where("key_hash = ? AND last_access IS NULL", hash).
			Delete(&model.S3ObjectMetadata{}).Error
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

// storeObjectMetadata keeps the metadata of an upload in the in-memory map and
// in the database, so that a HEAD answers from memory for the process that
// received the upload and from the database after a restart.
func (b *s3Backend) storeObjectMetadata(ctx context.Context, name string, headers map[string]string) {
	headers = storableMetadata(headers)
	// The object exists again: a delete that raced the batch in flight must not
	// drop the row written here (see accessTracker.present).
	b.access.present(name)
	b.cacheObjectMetadata(name, headers)
	b.storeStoredMetadata(ctx, name, headers)
}

// metaCacheLimit bounds the in-memory metadata cache. It holds one entry per
// object this process has written or read metadata for, so without a bound it
// would grow with the number of objects in every bucket. The cache is only an
// optimisation - a miss falls back to the database - so dropping it whole is
// always safe.
const metaCacheLimit = 8192

// cacheObjectMetadata records one entry and drops the whole cache once it grows
// past metaCacheLimit. Dropping everything instead of evicting one key keeps the
// bookkeeping to a counter: the map is shared by concurrent requests and any
// per-key eviction order would need more state than the cache is worth.
func (b *s3Backend) cacheObjectMetadata(name string, headers map[string]string) {
	b.metaMu.Lock()
	defer b.metaMu.Unlock()

	b.meta.Store(name, headers)
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

// loadObjectMetadata returns the metadata of an object, from the in-memory map
// when this process received the upload and from the database otherwise. A
// database hit repopulates the map, so only the first HEAD of an object after a
// restart pays for the query.
func (b *s3Backend) loadObjectMetadata(ctx context.Context, name string) (map[string]string, bool) {
	if value, ok := b.meta.Load(name); ok {
		if headers, ok := value.(map[string]string); ok {
			return headers, true
		}
	}
	headers, ok := b.loadStoredMetadata(ctx, name)
	if !ok {
		return nil, false
	}
	b.cacheObjectMetadata(name, headers)
	return headers, true
}

// copyStoredMetadata gives dst the user metadata recorded for src, which is
// what x-amz-metadata-directive: COPY asks for.
func (b *s3Backend) copyStoredMetadata(ctx context.Context, src, dst string) {
	headers, ok := b.loadStoredMetadata(ctx, src)
	if !ok {
		return
	}
	b.storeObjectMetadata(ctx, dst, headers)
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
		if existing, ok := b.loadObjectMetadata(ctx, name); ok {
			result.ETag = storedETag(existing)
		}
		return result, nil
	}
	// Everything the request describes replaces the record: the user metadata and
	// the object headers as well. Restricting this to the user metadata dropped a
	// Content-Type the request asked to set - a self-copy with REPLACE is the
	// documented way to change one without rewriting the bytes - and erased the
	// stored one at the same time, which the copy-to-another-key path does right.
	replaced := storableMetadata(meta)
	// The ETag and the whole-object checksums describe the content, which a
	// metadata replacement does not touch, so they must survive: dropping them
	// would make every later GET answer without them.
	if existing, ok := b.loadObjectMetadata(ctx, name); ok {
		if etag := storedETag(existing); etag != "" {
			replaced[etagMetadataKey] = strings.Trim(etag, "\"")
		}
		for key, value := range existing {
			if !isContentChecksumHeader(key) {
				continue
			}
			if _, present := replaced[key]; !present {
				replaced[key] = value
			}
		}
	}
	b.storeObjectMetadata(ctx, name, replaced)
	result.ETag = storedETag(replaced)

	// The in-memory entry of the process that received the upload still carries
	// the previous metadata, so it is rewritten to match.
	if value, ok := b.meta.Load(name); ok {
		if headers, isHeaders := value.(map[string]string); isHeaders {
			cleaned := map[string]string{}
			for key, value := range headers {
				if !strings.HasPrefix(strings.ToLower(key), userMetadataPrefix) {
					cleaned[key] = value
				}
			}
			for key, value := range replaced {
				cleaned[key] = value
			}
			b.cacheObjectMetadata(name, cleaned)
			result.ETag = storedETag(cleaned)
		}
	}

	return result, nil
}

// isContentChecksumHeader reports whether a metadata key names a checksum of the
// whole object, which a metadata-only replacement must not drop.
func isContentChecksumHeader(key string) bool {
	h := strings.ToLower(key)
	return strings.HasPrefix(h, "x-amz-checksum-") || h == "x-amz-sdk-checksum-algorithm"
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
