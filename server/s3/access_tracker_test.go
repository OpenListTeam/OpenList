package s3

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// accessRow returns the stored metadata row of the path, or nil.
func accessRow(t *testing.T, name string) *model.S3ObjectMetadata {
	t.Helper()
	var row model.S3ObjectMetadata
	err := db.GetDb().
		Where("key_hash = ?", model.HashS3ObjectMetadataKey(name)).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		t.Fatalf("load access row of %q: %v", name, err)
	}
	return &row
}

// setAccessTracking stores the switch so that a test can turn recording off.
func setAccessTracking(t *testing.T, value string) {
	t.Helper()
	item := &model.SettingItem{
		Key:   conf.S3AccessTrack,
		Value: value,
		Type:  conf.TypeBool,
		Group: model.S3,
	}
	if err := db.SaveSettingItem(item); err != nil {
		t.Fatalf("save %s: %v", conf.S3AccessTrack, err)
	}
	op.SettingCacheUpdate()
	t.Cleanup(func() {
		if err := db.GetDb().Where("key = ?", conf.S3AccessTrack).Delete(&model.SettingItem{}).Error; err != nil {
			t.Fatalf("delete %s: %v", conf.S3AccessTrack, err)
		}
		op.SettingCacheUpdate()
	})
}

// A read has to survive the process without costing a write on the request
// path: it is buffered, then written in bulk.
func TestAccessIsBufferedThenWritten(t *testing.T) {
	resetMetadataTable(t)
	b := &s3Backend{}

	before := time.Now().UTC()
	b.recordAccess("/nora/blobs/aa")
	if row := accessRow(t, "/nora/blobs/aa"); row != nil {
		t.Fatalf("read reached the database before the flush: %+v", row)
	}

	b.flushAccess()
	row := accessRow(t, "/nora/blobs/aa")
	if row == nil {
		t.Fatal("flush did not record the read")
	}
	if row.LastAccess == nil {
		t.Fatal("row has no last access time")
	}
	if row.LastAccess.Before(before.Add(-time.Second)) {
		t.Fatalf("last access %v is older than the read %v", row.LastAccess, before)
	}
}

// Recording a read must not disturb what the row already carries: user
// metadata and the recorded ETag stay exactly as they were.
func TestAccessKeepsObjectMetadata(t *testing.T) {
	resetMetadataTable(t)
	b := &s3Backend{}

	path := "/nora/blobs/bb"
	b.storeStoredMetadata(t.Context(), path, map[string]string{
		"x-amz-meta-sha256": "deadbeef",
	})

	b.recordAccess(path)
	b.flushAccess()

	row := accessRow(t, path)
	if row == nil || row.LastAccess == nil {
		t.Fatal("read was not recorded")
	}
	headers, ok := b.loadStoredMetadata(t.Context(), path)
	if !ok {
		t.Fatal("user metadata disappeared")
	}
	if headers["x-amz-meta-sha256"] != "deadbeef" {
		t.Fatalf("user metadata was overwritten: %v", headers)
	}
}

// An object that is read continuously is written once per interval, not once
// per read: a block device reads the same object thousands of times a minute.
func TestAccessIsThrottled(t *testing.T) {
	resetMetadataTable(t)
	b := &s3Backend{}

	path := "/nora/blobs/cc"
	b.recordAccess(path)
	b.flushAccess()
	first := accessRow(t, path)
	if first == nil || first.LastAccess == nil {
		t.Fatal("first read was not recorded")
	}

	// A second read within the interval must not produce a second write.
	b.recordAccess(path)
	b.flushAccess()
	second := accessRow(t, path)
	if second == nil || second.LastAccess == nil {
		t.Fatal("row disappeared after the second flush")
	}
	if !second.LastAccess.Equal(*first.LastAccess) {
		t.Fatalf("throttled read was written again: %v -> %v", first.LastAccess, second.LastAccess)
	}
}

// Turning the switch off stops the recording entirely.
func TestAccessTrackingCanBeDisabled(t *testing.T) {
	resetMetadataTable(t)
	setAccessTracking(t, "false")

	b := &s3Backend{}
	b.recordAccess("/nora/blobs/dd")
	b.flushAccess()

	if row := accessRow(t, "/nora/blobs/dd"); row != nil {
		t.Fatalf("read was recorded while tracking was off: %+v", row)
	}
}

// A buffered read of a deleted object must not bring its row back when the
// buffer is written.
func TestDeleteDropsBufferedAccess(t *testing.T) {
	ctx := t.Context()
	b, _ := setupMultipartBackend(t)
	b.access = accessTracker{}

	if _, err := b.PutObject(ctx, "mp", "gone.txt", map[string]string{"Content-Type": "text/plain"}, strings.NewReader("bye"), 3); err != nil {
		t.Fatalf("PutObject: %+v", err)
	}
	if _, err := b.HeadObject(ctx, "mp", "gone.txt"); err != nil {
		t.Fatalf("HeadObject: %+v", err)
	}
	if _, err := b.DeleteObject(ctx, "mp", "gone.txt"); err != nil {
		t.Fatalf("DeleteObject: %+v", err)
	}
	b.flushAccess()

	var count int64
	// The bucket path is the per-test temporary directory, so match on the key.
	if err := db.GetDb().Model(&model.S3ObjectMetadata{}).Where("path LIKE ?", "%gone.txt").Count(&count).Error; err != nil {
		t.Fatalf("count: %+v", err)
	}
	if count != 0 {
		t.Fatalf("the delete left %d row(s) behind", count)
	}
}
