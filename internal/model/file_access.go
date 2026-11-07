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
	// The composite (bucket, last_access) index is what the heatmap reads: it is
	// index-only, so "how many files per bucket per recency bucket" never touches
	// the table rows. It also serves the per-bucket filter on its own, which the
	// leftmost-prefix rule already covers.
	LastAccess *time.Time `json:"last_access" gorm:"column:last_access;index:idx_file_access_last;index:idx_file_access_bucket_last,priority:2"`
	// FirstAccess is when this instance first saw the file read, so that a
	// reader can tell "read once, yesterday" from "read daily for a year".
	FirstAccess *time.Time `json:"first_access"`
	// Hits counts reads, deduplicated into sessions: a media player issuing
	// dozens of ranged requests for one playback counts once. It answers
	// "frequently used", which a single timestamp cannot.
	Hits int64 `json:"hits"`
	// Bucket names the S3 bucket the most recent read came through, and is empty
	// for the readers that know nothing about buckets (/d, /p, WebDAV, the media
	// library). It exists because two buckets can front the same library at
	// different tiers: without it, "which bucket is this file hot in" could only
	// be answered by matching storage paths, and a bucket whose path is an alias
	// of another's would be indistinguishable. A row keeps the bucket of the last
	// read that named one, so a /d read does not erase what S3 recorded.
	Bucket string `json:"bucket" gorm:"size:64;index:idx_file_access_bucket;index:idx_file_access_bucket_last,priority:1"`
}

// TableName keeps the name short and singular, and - unlike every other table -
// it deliberately ignores conf.Database.TablePrefix. The index names declared on
// this model (idx_file_access, idx_file_access_last) are constants as well, so
// renaming the table on an existing install collides with the indexes of the old
// table and the migration aborts at startup ("index idx_file_access already
// exists"). Code that has to name the table in SQL resolves it through
// db.TableNameOf, never by hand.
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
