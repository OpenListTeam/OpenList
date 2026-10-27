package db

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestMigrateS3MarkersBackfillsHashesAndRebuildsIdentity(t *testing.T) {
	d := openTestDB(t, "ol_")
	db = d
	if err := d.Exec(`CREATE TABLE ol_s3_markers (
		id integer primary key autoincrement,
		bucket text,
		key text,
		key_hash text,
		metadata text,
		modified datetime
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.Exec(`CREATE INDEX idx_s3_marker_bucket_key_hash ON ol_s3_markers (bucket, key_hash)`).Error; err != nil {
		t.Fatal(err)
	}
	insert := func(bucket, key, metadata string) {
		t.Helper()
		hash := sha256.Sum256([]byte(key))
		if err := d.Exec("INSERT INTO ol_s3_markers (bucket, key, key_hash, metadata) VALUES (?, ?, ?, ?)", bucket, key, hex.EncodeToString(hash[:]), metadata).Error; err != nil {
			t.Fatal(err)
		}
	}
	insert("first", "shared/", "old")
	insert("second", "shared/", "other bucket")
	insert("first", "shared/", "latest")

	if err := migrateS3Markers(); err != nil {
		t.Fatal(err)
	}
	if err := d.AutoMigrate(&model.S3Marker{}); err != nil {
		t.Fatal(err)
	}
	if d.Migrator().HasIndex("ol_s3_markers", "idx_s3_marker_bucket_key_hash") {
		t.Fatal("legacy composite index was not removed")
	}
	if !d.Migrator().HasIndex("ol_s3_markers", "idx_s3_marker_key_hash") {
		t.Fatal("bucket-aware unique hash index was not created")
	}
	var rows []model.S3Marker
	if err := d.Order("bucket").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("migrated rows = %d, want one row per bucket/key pair", len(rows))
	}
	for _, row := range rows {
		if row.KeyHash != model.HashS3MarkerKey(row.Bucket, row.Key) {
			t.Errorf("migrated hash for %s/%s = %q, want bucket-aware hash", row.Bucket, row.Key, row.KeyHash)
		}
	}
	if rows[0].Metadata != "latest" {
		t.Fatalf("duplicate marker metadata = %q, want latest", rows[0].Metadata)
	}
	duplicate := model.S3Marker{Bucket: "first", Key: "shared/", KeyHash: model.HashS3MarkerKey("first", "shared/")}
	if err := d.Create(&duplicate).Error; err == nil {
		t.Fatal("migrated index accepted a duplicate bucket/key pair")
	}
	if err := migrateS3Markers(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
}
