package model

import (
	"crypto/sha256"
	"encoding/hex"

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
