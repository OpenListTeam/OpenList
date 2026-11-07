package model

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// FileAccess records when a file was last read and how often, for every read
// path that serves content: the direct link (/d), the proxy (/p), WebDAV and
// the media library (which hands out /d and /p links).
//
// The storage drivers expose a modification time but no access time, and a
// modification time cannot answer "is anybody still using this": a file written
// once and read every day looks as cold as one nobody ever opened. A hot/cold
// split therefore needs this table, which is written by the server and read by
// whatever decides to move files between tiers.
//
// Path is stored as TEXT so that long paths are never truncated, and the unique
// index uses a SHA-256 hash of the whole path because MySQL cannot index a TEXT
// column without a key prefix length - the same arrangement S3ObjectMetadata and
// WebDAVProperty use.
type FileAccess struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// Path is the full path inside this instance, storage mount included, for
	// example /本机储存/热层/movie.mkv.
	Path     string `json:"path" gorm:"type:text"`
	PathHash string `json:"-" gorm:"size:64;uniqueIndex:idx_file_access"`
	// LastAccess is the most recent read. It is never NULL: a row only exists
	// because the file was read at least once.
	LastAccess *time.Time `json:"last_access" gorm:"column:last_access;index:idx_file_access_last"`
	// FirstAccess is when this instance first saw the file read, so that a
	// reader can tell "read once, yesterday" from "read daily for a year".
	FirstAccess *time.Time `json:"first_access"`
	// Hits counts reads, deduplicated into sessions: a media player issuing
	// dozens of ranged requests for one playback counts once. It answers
	// "frequently used", which a single timestamp cannot.
	Hits int64 `json:"hits"`
}

// TableName keeps the table name short and singular; the SQL of the bulk upsert
// refers to it by name.
func (FileAccess) TableName() string {
	return "file_access"
}

func (f *FileAccess) BeforeSave(*gorm.DB) error {
	f.PathHash = HashFilePath(f.Path)
	return nil
}

// HashFilePath returns a stable hash of a path, used as the unique key of the
// access table.
func HashFilePath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}
