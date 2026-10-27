package s3

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OpenListTeam/gofakes3"
	"gorm.io/gorm"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
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
	return headers, true
}

// storeStoredMetadata records the user metadata of an object. An object that
// carries none owns no row, which is what keeps the table empty for the vast
// majority of objects.
func (b *s3Backend) storeStoredMetadata(ctx context.Context, name string, headers map[string]string) {
	if !hasUserMetadata(headers) {
		b.deleteStoredMetadata(ctx, name)
		return
	}
	encoded, err := json.Marshal(headers)
	if err != nil {
		return
	}
	err = withS3MetadataTransaction(ctx, func(tx *gorm.DB) error {
		row := model.S3ObjectMetadata{
			Path:    name,
			Headers: encoded,
		}
		return tx.Where("key_hash = ?", model.HashS3ObjectMetadataKey(name)).
			Assign(map[string]any{"headers": row.Headers}).
			FirstOrCreate(&row).Error
	})
	if err != nil {
		log.Debugf("s3: store metadata for %q: %v", name, err)
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
	b.meta.Store(name, headers)
	b.storeStoredMetadata(ctx, name, headers)
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
	b.meta.Store(name, headers)
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

	// A request that carries user metadata describes the new metadata in full.
	// The same is true of an explicit REPLACE with none, which is the documented
	// way to strip an object's metadata without rewriting its content; a plain
	// self-copy asks for the metadata to be left alone.
	replacing := hasUserMetadata(meta) || strings.EqualFold(metadataDirective(meta), "REPLACE")
	if !replacing {
		return result, nil
	}
	replaced := userMetadataOf(meta)
	b.storeStoredMetadata(ctx, name, replaced)

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
			b.meta.Store(name, cleaned)
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
