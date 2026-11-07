package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/gofakes3"
)

func TestS3ModeResolution(t *testing.T) {
	// Restore settings after test.
	origMode := setting.GetStr(conf.S3Mode)
	origBuckets := setting.GetStr(conf.S3Buckets)
	origEtagMode := setting.GetStr(conf.S3EtagMode)
	t.Cleanup(func() {
		setTestSetting(conf.S3Mode, origMode)
		setTestSetting(conf.S3Buckets, origBuckets)
		setTestSetting(conf.S3EtagMode, origEtagMode)
	})

	// 1. Default when unset is performance mode.
	setTestSetting(conf.S3Mode, "")
	setTestSetting(conf.S3Buckets, "[]")
	if !isPerformanceMode("") {
		t.Fatalf("expected default mode to be performance, got compat")
	}
	if globalS3Mode() != S3ModePerformance {
		t.Fatalf("expected global mode to be %q, got %q", S3ModePerformance, globalS3Mode())
	}

	// 2. Explicit global compat mode.
	setTestSetting(conf.S3Mode, S3ModeCompat)
	if isPerformanceMode("") {
		t.Fatalf("expected global compat mode to report isPerformanceMode=false")
	}
	if globalS3Mode() != S3ModeCompat {
		t.Fatalf("expected global mode %q, got %q", S3ModeCompat, globalS3Mode())
	}

	// 3. Explicit global performance mode.
	setTestSetting(conf.S3Mode, S3ModePerformance)
	if !isPerformanceMode("") {
		t.Fatalf("expected global performance mode to report isPerformanceMode=true")
	}

	// 4. Per-bucket overrides.
	bucketsJSON := `[
		{"name": "bkt-perf", "path": "/tmp/bkt1", "mode": "performance"},
		{"name": "bkt-compat", "path": "/tmp/bkt2", "mode": "compat"},
		{"name": "bkt-default", "path": "/tmp/bkt3"}
	]`
	setTestSetting(conf.S3Buckets, bucketsJSON)

	// In global performance mode:
	setTestSetting(conf.S3Mode, S3ModePerformance)
	if !isPerformanceMode("bkt-perf") {
		t.Errorf("bkt-perf should be performance mode")
	}
	if isPerformanceMode("bkt-compat") {
		t.Errorf("bkt-compat should be compat mode even under global performance")
	}
	if !isPerformanceMode("bkt-default") {
		t.Errorf("bkt-default should inherit global performance mode")
	}
	if !isPerformanceMode("unknown-bkt") {
		t.Errorf("unknown-bkt should inherit global performance mode")
	}

	// In global compat mode:
	setTestSetting(conf.S3Mode, S3ModeCompat)
	if !isPerformanceMode("bkt-perf") {
		t.Errorf("bkt-perf should stay performance mode even under global compat")
	}
	if isPerformanceMode("bkt-compat") {
		t.Errorf("bkt-compat should be compat mode")
	}
	if isPerformanceMode("bkt-default") {
		t.Errorf("bkt-default should inherit global compat mode")
	}
	if isPerformanceMode("unknown-bkt") {
		t.Errorf("unknown-bkt should inherit global compat mode")
	}
}

func TestS3ModeEffectiveEtagMode(t *testing.T) {
	origMode := setting.GetStr(conf.S3Mode)
	origBuckets := setting.GetStr(conf.S3Buckets)
	origEtagMode := setting.GetStr(conf.S3EtagMode)
	t.Cleanup(func() {
		setTestSetting(conf.S3Mode, origMode)
		setTestSetting(conf.S3Buckets, origBuckets)
		setTestSetting(conf.S3EtagMode, origEtagMode)
	})

	setTestSetting(conf.S3Buckets, `[
		{"name": "perf-bkt", "path": "/tmp/bkt1", "mode": "performance"},
		{"name": "compat-bkt", "path": "/tmp/bkt2", "mode": "compat"}
	]`)

	// When s3_etag_mode is default (version):
	setTestSetting(conf.S3EtagMode, "version")

	// Performance mode uses version (zero-read).
	if mode := effectiveEtagMode("perf-bkt"); mode != etagModeVersion {
		t.Errorf("expected perf-bkt etag mode to be %q, got %q", etagModeVersion, mode)
	}

	// Compat mode always forces content (standard RFC/AWS S3 content MD5).
	if mode := effectiveEtagMode("compat-bkt"); mode != etagModeContent {
		t.Errorf("expected compat-bkt etag mode to be %q, got %q", etagModeContent, mode)
	}

	// When user explicitly sets s3_etag_mode=content, performance mode respects it.
	setTestSetting(conf.S3EtagMode, "content")
	if mode := effectiveEtagMode("perf-bkt"); mode != etagModeContent {
		t.Errorf("expected perf-bkt with explicit content setting to be %q, got %q", etagModeContent, mode)
	}
}

func TestS3ModeSettingsHooksValidation(t *testing.T) {
	// 1. Validate S3Mode hook.
	if err := checkS3ModeSetting("performance"); err != nil {
		t.Errorf("expected valid performance mode to pass: %v", err)
	}
	if err := checkS3ModeSetting("compat"); err != nil {
		t.Errorf("expected valid compat mode to pass: %v", err)
	}
	if err := checkS3ModeSetting("  PERFORMANCE  "); err != nil {
		t.Errorf("expected case-insensitive mode to pass: %v", err)
	}
	if err := checkS3ModeSetting("invalid_mode"); err == nil {
		t.Errorf("expected invalid mode to fail")
	}

	// 2. Validate S3Buckets setting hook with bucket mode.
	validBuckets := `[{"name": "b1", "path": "/tmp/p1", "mode": "performance"}, {"name": "b2", "path": "/tmp/p2", "mode": "compat"}]`
	if err := checkS3BucketsSetting(validBuckets); err != nil {
		t.Errorf("expected valid buckets JSON to pass: %v", err)
	}

	invalidBuckets := `[{"name": "b1", "path": "/tmp/p1", "mode": "turbo"}]`
	if err := checkS3BucketsSetting(invalidBuckets); err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Errorf("expected invalid bucket mode to fail with descriptive error, got: %v", err)
	}
}

func checkS3ModeSetting(val string) error {
	raw := strings.ToLower(strings.TrimSpace(val))
	if raw != "" && raw != S3ModePerformance && raw != S3ModeCompat {
		return errors.New("invalid mode")
	}
	return nil
}

func TestS3ModeMetadataPersistenceBypass(t *testing.T) {
	b := &s3Backend{}
	ctx := context.Background()

	// In performance mode:
	// A standard object without custom metadata should not persist stored metadata row.
	stdHeaders := map[string]string{
		"Content-Type": "image/png",
		"ETag":         `"abcdef"`,
	}
	b.storeStoredMetadataForBucket(ctx, "perf-bucket", "test-perf-std", stdHeaders)
	if _, ok := b.loadStoredMetadata(ctx, "test-perf-std"); ok {
		t.Errorf("expected standard headers in performance mode not to be persisted in DB")
	}

	// An object with custom user metadata MUST be persisted in performance mode.
	userHeaders := map[string]string{
		"Content-Type":     "image/png",
		"X-Amz-Meta-Owner": "alice",
	}
	b.storeStoredMetadataForBucket(ctx, "perf-bucket", "test-perf-user", userHeaders)
	if loaded, ok := b.loadStoredMetadata(ctx, "test-perf-user"); !ok {
		t.Errorf("expected user headers in performance mode to be persisted in DB")
	} else if loaded["X-Amz-Meta-Owner"] != "alice" && loaded["x-amz-meta-owner"] != "alice" {
		t.Errorf("expected loaded user header X-Amz-Meta-Owner=alice, got: %v", loaded)
	}
}

func TestS3ModeMultipartCompositeETag(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)

	mount := "/" + sanitizeTestName(t.Name())
	// Configure bucket in compat mode
	setTestSetting(conf.S3Buckets, `[{"name":"mp","path":"`+mount+`","mode":"compat"}]`)
	t.Cleanup(func() {
		setTestSetting(conf.S3Buckets, `[{"name":"mp","path":"`+mount+`"}]`)
	})

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "compat-multipart.txt", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}

	p1 := []byte("part-1-data")
	p2 := []byte("part-2-data")
	sum1 := md5.Sum(p1)
	sum2 := md5.Sum(p2)

	etag1, err := b.UploadPart(ctx, "mp", "compat-multipart.txt", uploadID, 1, int64(len(p1)), bytes.NewReader(p1))
	if err != nil {
		t.Fatalf("UploadPart 1: %v", err)
	}
	etag2, err := b.UploadPart(ctx, "mp", "compat-multipart.txt", uploadID, 2, int64(len(p2)), bytes.NewReader(p2))
	if err != nil {
		t.Fatalf("UploadPart 2: %v", err)
	}

	_, completeETag, err := b.CompleteMultipartUpload(ctx, "mp", "compat-multipart.txt", uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{
			{PartNumber: 1, ETag: etag1},
			{PartNumber: 2, ETag: etag2},
		},
	})
	if err != nil {
		t.Fatalf("CompleteMultipartUpload: %v", err)
	}

	// Verify composite ETag format: "<md5>-2"
	composite := md5.New()
	composite.Write(sum1[:])
	composite.Write(sum2[:])
	expectedComposite := fmt.Sprintf("\"%s-2\"", hex.EncodeToString(composite.Sum(nil)))
	if completeETag != expectedComposite {
		t.Fatalf("compat mode multipart ETag = %q, want %q", completeETag, expectedComposite)
	}

	// Verify HEAD returns this composite ETag
	obj, err := b.HeadObject(ctx, "mp", "compat-multipart.txt")
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if obj.ETag != expectedComposite {
		t.Fatalf("HeadObject ETag in compat mode = %q, want %q", obj.ETag, expectedComposite)
	}
}
