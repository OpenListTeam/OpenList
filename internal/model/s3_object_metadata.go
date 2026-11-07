package model

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// S3ObjectMetadata stores the user metadata (x-amz-meta-*) of one object served
// by the built-in S3 server.
//
// S3 metadata has no faithful counterpart in the storage drivers: most remote
// drivers have nowhere to keep it, and extended attributes only exist for files
// on this machine. It is therefore persisted here instead of next to the
// content.
//
// Path is a TEXT column so that long keys are never truncated or rejected.
// MySQL cannot index TEXT columns without a key prefix length, so the unique
// index uses a SHA-256 hash of the complete path instead - the same arrangement
// WebDAVProperty uses for its property key.
type S3ObjectMetadata struct {
	ID      uint   `json:"id" gorm:"primaryKey"`
	Path    string `json:"path" gorm:"type:text"`
	KeyHash string `json:"-" gorm:"size:64;uniqueIndex:idx_s3_object_metadata"`
	Headers []byte `json:"headers"`
	// LastAccess is when the S3 server last served a read of the object: a GET
	// (full or ranged) or a HEAD. The storage drivers keep a modification time
	// but no access time, and "was this read recently" exists nowhere but here -
	// which is what a hot/cold split needs. A row of a never read object keeps
	// it NULL.
	LastAccess *time.Time `json:"last_access" gorm:"column:last_access;index:idx_s3_object_metadata_access"`
	// ETag is the ETag of the object as the S3 server last reported it, recorded
	// so that a later HEAD can answer without reading the object: a remote
	// storage pays a full download for every hash it has to compute, which turns
	// one 128 KiB block read into a download of the whole object.
	ETag string `json:"etag" gorm:"column:etag;size:64"`
	// Size and ModTime identify the content the ETag describes. They come from
	// the node right after the upload, so a later read only has to compare them
	// to tell whether the object was changed behind the server's back.
	Size    int64 `json:"size"`
	ModTime int64 `json:"modtime"`
}

func (m *S3ObjectMetadata) BeforeSave(*gorm.DB) error {
	m.KeyHash = HashS3ObjectMetadataKey(m.Path)
	return nil
}

// HashS3ObjectMetadataKey returns a stable hash for the object path.
func HashS3ObjectMetadataKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}
