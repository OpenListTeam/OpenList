package model

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// S3Marker stores a trailing-slash S3 object without collapsing it into a directory path.
type S3Marker struct {
	ID       uint   `gorm:"primaryKey"`
	Bucket   string `gorm:"size:191;not null;index:idx_s3_marker_bucket"`
	Key      string `gorm:"type:text;not null"`
	KeyHash  string `gorm:"size:64;not null;uniqueIndex:idx_s3_marker_key_hash"`
	Metadata string `gorm:"type:text"`
	Modified time.Time
}

// HashS3MarkerKey returns the indexed identity of an exact bucket/key pair.
func HashS3MarkerKey(bucket, key string) string {
	hash := sha256.Sum256([]byte(bucket + "\x00" + key))
	return hex.EncodeToString(hash[:])
}
