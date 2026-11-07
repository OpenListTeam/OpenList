package s3

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	log "github.com/sirupsen/logrus"
)

// The ETag of an object can be established in two ways.
//
// "content" is the S3-compatible one: the MD5 of the complete object. Its
// property is that equal content always yields an equal ETag, but it also has to
// be recomputed after a restart, and recomputing it means reading the object top
// to bottom. On a remote storage that is a full download per object, and because
// every block read of a block-device client is a range request, it turns each
// read of one block into a download of the whole object.
//
// "version" keeps the ETag stable without ever reading an object: an object
// written through this server has the ETag its upload already produced on record,
// and an object that has no record - uploaded before this scheme existed, or by
// a client that bypasses the S3 server - gets one derived from its key, size and
// modification time, stored on first access. What it gives up is that two objects
// with equal content no longer share an ETag; what it keeps is everything a
// conditional write needs, namely that the ETag changes when the object changes
// and survives a restart.
const (
	etagModeContent = "content"
	etagModeVersion = "version"
)

// currentEtagMode returns the configured ETag scheme. "version" is the default,
// because it is the only scheme that never has to read an object; "content" stays
// available for clients that need the S3-compatible content hash. An empty or
// unrecognised value resolves to the default rather than to the expensive one.
func currentEtagMode() string {
	if strings.EqualFold(strings.TrimSpace(setting.GetStr(conf.S3EtagMode)), etagModeContent) {
		return etagModeContent
	}
	return etagModeVersion
}

// contentFingerprint identifies the content an ETag describes.
//
// The modification time keeps its full precision. Truncating it to seconds made
// a rewrite that preserved the size and landed in the same second indistinguishable
// from the object it replaced: the recorded ETag was reused, a conditional write
// carrying the stale ETag passed its If-Match, and the write it was supposed to
// lose silently overwrote the other writer's data. A truncation that is merely
// conservative (reporting a change that did not happen) would only cost one
// re-derivation, so precision is the safe side to err on.
func contentFingerprint(size, modTime int64) (int64, int64) {
	return size, modTime
}

// derivedEtag is the ETag of an object that has none on record. It depends only
// on the key, its size and its modification time, so it stays the same across a
// restart and changes whenever the object is replaced.
//
// The modification time is the one contentFingerprint produced - nanoseconds, as
// the driver reports them - so that a rewrite inside the same second still yields
// a new ETag.
func derivedEtag(name string, size, modTimeSec int64) []byte {
	hash := md5.New()
	hash.Write([]byte(name))
	hash.Write([]byte{0})
	hash.Write(binary.BigEndian.AppendUint64(nil, uint64(size)))
	hash.Write(binary.BigEndian.AppendUint64(nil, uint64(modTimeSec)))
	return hash.Sum(nil)
}

// loadStoredRow returns the row of an object, if it has one.
func loadStoredRow(ctx context.Context, name string) (model.S3ObjectMetadata, bool) {
	database := db.GetDb()
	if database == nil {
		return model.S3ObjectMetadata{}, false
	}
	var row model.S3ObjectMetadata
	err := database.WithContext(ctx).
		Where("key_hash = ?", model.HashS3ObjectMetadataKey(name)).
		Take(&row).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Debugf("s3: load row for %q: %v", name, err)
		}
		return model.S3ObjectMetadata{}, false
	}
	return row, true
}

// recordIdentity stores the ETag of an object together with the size and
// modification time it describes. It runs on the upload path, where the content
// was hashed on the way in anyway, and on the read path the first time an object
// without a recorded ETag is met.
//
// Size and modification time are given as contentFingerprint produces them; the
// callers normalise once so that what is written is exactly what
// persistedIdentityHash compares against. A row written by an older build holds
// the modification time in seconds, which no longer compares equal: such an object
// gets its ETag derived once more and the row is rewritten with the new precision.
func (b *s3Backend) recordIdentity(ctx context.Context, name, etag string, size, modTimeSec int64) {
	err := withS3MetadataTransaction(ctx, func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key_hash"}},
			DoUpdates: clause.AssignmentColumns([]string{"etag", "size", "mod_time"}),
		}).Create(&model.S3ObjectMetadata{Path: name, ETag: etag, Size: size, ModTime: modTimeSec}).Error
	})
	if err != nil {
		log.Debugf("s3: record etag for %q: %v", name, err)
	}
}

// persistedIdentityHash returns the ETag of an object without reading it.
//
// An object this server wrote has its ETag on record; anything else gets one
// derived from what the listing already told us. Either way the answer is kept,
// so meeting a new object costs one database write instead of a download - which
// is the point, since an object without a recorded ETag otherwise costs a full
// download per HEAD and per range read.
func (b *s3Backend) persistedIdentityHash(ctx context.Context, name string, size, modTimeNanos int64) []byte {
	size, modTimeSec := contentFingerprint(size, modTimeNanos)

	if row, ok := loadStoredRow(ctx, name); ok && row.ETag != "" && row.Size == size && row.ModTime == modTimeSec {
		if hash, err := hex.DecodeString(row.ETag); err == nil && len(hash) == md5.Size {
			return hash
		}
		// A recorded ETag this build cannot read is replaced below rather than
		// reported as an empty identity.
	}

	derived := derivedEtag(name, size, modTimeSec)
	b.recordIdentity(ctx, name, hex.EncodeToString(derived), size, modTimeSec)
	return derived
}

// describeEtagMode names the active scheme for the startup log, so that an
// operator can see whether the server answers a HEAD without touching the
// objects.
func describeEtagMode() string {
	if currentEtagMode() == etagModeVersion {
		return "version (answers from the database and never reads an object to compute an ETag)"
	}
	return "content (MD5 of the object; an object without a recorded hash is read in full)"
}
