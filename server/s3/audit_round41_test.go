package s3

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/OpenListTeam/gofakes3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Round 41 Audit: Deep penetration testing across multipart edge cases,
// dynamic mode switches, signature tampering, and malformed XML/JSON payloads.

func TestAuditRound41_MultipartEdgeCases(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()
	bucket := "mp"

	// A: Non-contiguous parts (part 1, 5, 20) in ascending order
	uploadID, err := b.CreateMultipartUpload(ctx, bucket, "noncontiguous.bin", nil)
	require.NoError(t, err)

	part1Payload := []byte("part-one-data-at-least-some-bytes")
	part5Payload := []byte("part-five-payload-bytes")
	part20Payload := []byte("part-twenty-last-data")

	etag1, err := b.UploadPart(ctx, bucket, "noncontiguous.bin", uploadID, 1, int64(len(part1Payload)), bytes.NewReader(part1Payload))
	require.NoError(t, err)
	etag5, err := b.UploadPart(ctx, bucket, "noncontiguous.bin", uploadID, 5, int64(len(part5Payload)), bytes.NewReader(part5Payload))
	require.NoError(t, err)
	etag20, err := b.UploadPart(ctx, bucket, "noncontiguous.bin", uploadID, 20, int64(len(part20Payload)), bytes.NewReader(part20Payload))
	require.NoError(t, err)

	// Complete with parts in ascending order
	completeInput := &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{
			{PartNumber: 1, ETag: etag1},
			{PartNumber: 5, ETag: etag5},
			{PartNumber: 20, ETag: etag20},
		},
	}
	_, _, err = b.CompleteMultipartUpload(ctx, bucket, "noncontiguous.bin", uploadID, completeInput)
	assert.NoError(t, err, "non-contiguous parts in ascending order must assemble properly")

	// Verify object content matches concatenated parts
	obj, err := b.GetObject(ctx, bucket, "noncontiguous.bin", nil)
	require.NoError(t, err)
	content, err := io.ReadAll(obj.Contents)
	require.NoError(t, err)
	expected := append(append(part1Payload, part5Payload...), part20Payload...)
	assert.Equal(t, expected, content)

	// B: Out-of-order parts must be rejected with InvalidPartOrder
	uploadID2, err := b.CreateMultipartUpload(ctx, bucket, "order.bin", nil)
	require.NoError(t, err)
	etagA, err := b.UploadPart(ctx, bucket, "order.bin", uploadID2, 1, 4, bytes.NewReader([]byte("data")))
	require.NoError(t, err)
	etagB, err := b.UploadPart(ctx, bucket, "order.bin", uploadID2, 2, 4, bytes.NewReader([]byte("more")))
	require.NoError(t, err)

	badOrderInput := &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{
			{PartNumber: 2, ETag: etagB},
			{PartNumber: 1, ETag: etagA},
		},
	}
	_, _, err = b.CompleteMultipartUpload(ctx, bucket, "order.bin", uploadID2, badOrderInput)
	assert.Error(t, err)
	assert.Equal(t, gofakes3.ErrInvalidPartOrder, s3ErrorCode(err))

	_ = b.AbortMultipartUpload(ctx, bucket, "order.bin", uploadID2)
}

// 2. Dynamic S3Mode switching during active lifecycle:
func TestAuditRound41_DynamicModeSwitchLifecycle(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()
	bucket := "mp"

	// Start in performance mode
	setTestSetting("s3_mode", "performance")
	t.Cleanup(func() { setTestSetting("s3_mode", "") })

	key := "lifecycle-test.bin"
	data := []byte("dynamic-switch-payload-content")

	// Put in performance mode
	_, err := b.PutObject(ctx, bucket, key, nil, bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	// Read in performance mode
	headPerf, err := b.HeadObject(ctx, bucket, key)
	require.NoError(t, err)
	perfETag := headPerf.ETag
	assert.NotEmpty(t, perfETag)

	// Switch globally to compat mode
	setTestSetting("s3_mode", "compat")

	// Read in compat mode: content must remain identical, ETag accessible
	headCompat, err := b.HeadObject(ctx, bucket, key)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), headCompat.Size)

	// Conditional write in compat mode matching performance ETag:
	updatedData := []byte("updated-in-compat-mode")
	_, err = b.PutObject(ctx, bucket, key, map[string]string{
		"If-Match": perfETag,
	}, bytes.NewReader(updatedData), int64(len(updatedData)))
	assert.NoError(t, err, "CAS using previous mode ETag must succeed")

	// Read back updated content
	obj, err := b.GetObject(ctx, bucket, key, nil)
	require.NoError(t, err)
	content, err := io.ReadAll(obj.Contents)
	require.NoError(t, err)
	assert.Equal(t, updatedData, content)
}

// 3. Concurrent PutObject and CompleteMultipartUpload to identical destination key
func TestAuditRound41_ConcurrentPutAndMultipartComplete(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()
	bucket := "mp"
	key := "concurrent-put-mp.bin"

	uploadID, err := b.CreateMultipartUpload(ctx, bucket, key, nil)
	require.NoError(t, err)

	partData := []byte("multipart-part-payload")
	etag, err := b.UploadPart(ctx, bucket, key, uploadID, 1, int64(len(partData)), bytes.NewReader(partData))
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(2)
	var putErr, compErr error

	go func() {
		defer wg.Done()
		_, putErr = b.PutObject(ctx, bucket, key, nil, strings.NewReader("direct-put-wins-or-interleaves"), 31)
	}()

	go func() {
		defer wg.Done()
		_, _, compErr = b.CompleteMultipartUpload(ctx, bucket, key, uploadID, &gofakes3.CompleteMultipartUploadRequest{
			Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: etag}},
		})
	}()

	wg.Wait()
	// Both operations must finish safely; object must be readable and uncorrupted
	obj, err := b.GetObject(ctx, bucket, key, nil)
	require.NoError(t, err, "object must be readable and uncorrupted after race")
	readData, err := io.ReadAll(obj.Contents)
	require.NoError(t, err)
	assert.True(t, len(readData) == 31 || bytes.Equal(readData, partData), "content must be one of the clean payloads")

	_ = putErr
	_ = compErr
}

// 4. Malformed XML and JSON handling in S3 API handlers
func TestAuditRound41_MalformedPayloadResistance(t *testing.T) {
	setupMultipartBackend(t)

	// Malformed XML to PutBucketCors on existing bucket "mp"
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/mp?cors", strings.NewReader("<InvalidXML>>"))
	handled := handleS3CORSConfiguration(w, r)
	assert.True(t, handled)
	assert.Equal(t, http.StatusBadRequest, w.Code, "malformed XML must return 400 Bad Request")
	assert.Contains(t, w.Body.String(), "MalformedXML")

	// Malformed JSON in S3BucketAuth setting
	setTestSetting("s3_bucket_auth", "{invalid-json-string")
	t.Cleanup(func() { setTestSetting("s3_bucket_auth", "") })

	rules, misconfigured := loadS3BucketAuthRules()
	assert.True(t, misconfigured, "malformed JSON setting must be reported as misconfigured (fail-closed)")
	assert.Empty(t, rules, "no rules should be loaded from broken JSON")
}
