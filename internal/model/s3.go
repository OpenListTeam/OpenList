package model

import "time"

// S3Marker stores a trailing-slash S3 object without collapsing it into a directory path.
type S3Marker struct {
	ID       uint   `gorm:"primaryKey"`
	Bucket   string `gorm:"size:191;not null;index:idx_s3_marker_bucket"`
	Key      string `gorm:"type:text;not null"`
	KeyHash  string `gorm:"size:64;not null;uniqueIndex:idx_s3_marker_key_hash"`
	Metadata string `gorm:"type:text"`
	Modified time.Time
}
