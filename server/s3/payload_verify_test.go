package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/OpenListTeam/gofakes3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wireCode returns the S3 error code an error carries, or "" when it carries none.
func wireCode(err error) gofakes3.ErrorCode {
	code, _ := asS3ErrorCode(err)
	return code
}

func ctxDeclaringPayload(ctx context.Context, payload string) context.Context {
	sum := sha256.Sum256([]byte(payload))
	return context.WithValue(ctx, payloadHashKey{}, hex.EncodeToString(sum[:]))
}

// A PUT whose body is shorter than its declared length used to answer 200 and
// leave an object that is truncated relative to what the client signed for.
func TestPutObjectShortBodyIsRejected(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	_, err := b.PutObject(ctx, "mp", "pv/short.bin", nil, strings.NewReader("abc"), 10)
	require.Error(t, err)
	assert.Equal(t, gofakes3.ErrIncompleteBody, wireCode(err))

	_, err = b.HeadObject(ctx, "mp", "pv/short.bin")
	require.Error(t, err, "the rejected body must not be published")
}

// gofakes3 hands the storage a LimitReader sized from the declared length, so a
// request wrapper that waits for EOF may never learn that the payload differs.
// The backend therefore verifies what it actually writes.
func TestPutObjectSubstitutedPayloadIsRejected(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := ctxDeclaringPayload(context.Background(), "the-signed-payload")

	// Same length as the signed payload, different bytes: exactly what a
	// substitution attack looks like on the wire.
	hacked := "the-hacked-payload"
	require.Len(t, hacked, len("the-signed-payload"))
	_, err := b.PutObject(ctx, "mp", "pv/sub.bin", nil, strings.NewReader(hacked), int64(len(hacked)))
	require.Error(t, err)
	assert.Equal(t, gofakes3.ErrBadDigest, wireCode(err))

	_, err = b.HeadObject(ctx, "mp", "pv/sub.bin")
	require.Error(t, err, "unsigned bytes must not become an object")
}

// A rejected PUT must never publish the bytes it rejected. The storage layer
// cannot roll back to the previous version, so the key ends up absent rather
// than holding content nobody signed; this test pins that choice down.
func TestPutObjectPayloadMismatchRemovesTheKey(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	good := ctxDeclaringPayload(context.Background(), "original")

	_, err := b.PutObject(good, "mp", "pv/keep.bin", nil, strings.NewReader("original"), int64(len("original")))
	require.NoError(t, err)

	bad := ctxDeclaringPayload(context.Background(), "something-else")
	_, err = b.PutObject(bad, "mp", "pv/keep.bin", nil, strings.NewReader("substituted"), int64(len("substituted")))
	require.Error(t, err)

	// The upload is committed by renaming a temporary over the key, so a body
	// that fails its hash never replaces what was there.
	obj, err := b.GetObject(context.Background(), "mp", "pv/keep.bin", nil)
	require.NoError(t, err, "the rejected upload must not touch the existing key")
	body, err := io.ReadAll(obj.Contents)
	require.NoError(t, err)
	require.Equal(t, "original", string(body))

	// And the temporary it streamed into is gone.
	entries, err := b.ListBucket(context.Background(), "mp", &gofakes3.Prefix{Prefix: "pv/"}, gofakes3.ListBucketPage{})
	require.NoError(t, err)
	for _, item := range entries.Contents {
		require.NotContains(t, item.Key, s3TempKeyPrefix, "a temporary key survived the rejected upload")
	}
}

// A matching payload still has to work, obviously.
func TestPutObjectMatchingPayloadIsAccepted(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := ctxDeclaringPayload(context.Background(), "fine")

	_, err := b.PutObject(ctx, "mp", "pv/ok.bin", nil, strings.NewReader("fine"), 4)
	require.NoError(t, err)

	obj, err := b.HeadObject(ctx, "mp", "pv/ok.bin")
	require.NoError(t, err)
	assert.Equal(t, int64(4), obj.Size)
}

// CompleteMultipartUpload assembles the object from the uploaded parts, so the
// request body (the XML part list) says nothing about the bytes written. When
// the assembled content was verified against that body's declared hash, every
// completion from a client that signs payloads was rejected with BadDigest —
// boto3 signs, so multipart uploads were simply broken.
func TestCompleteMultipartIgnoresRequestBodyHash(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	// The hash of the request body, exactly as the payload guard hands it down.
	ctx := ctxDeclaringPayload(context.Background(), "<CompleteMultipartUpload/>")

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", "pv/complete.bin", nil)
	require.NoError(t, err)
	// The parts are separate requests, each with a body of its own: a part whose
	// x-amz-content-sha256 does not describe its bytes is refused (see
	// TestUploadPartVerifiesTheDeclaredPayloadHash), so the declared hash of the
	// completion body must not be handed to them here.
	partCtx := context.Background()
	etag1, err := b.UploadPart(partCtx, "mp", "pv/complete.bin", uploadID, 1, 6, strings.NewReader("first-"))
	require.NoError(t, err)
	etag2, err := b.UploadPart(partCtx, "mp", "pv/complete.bin", uploadID, 2, 6, strings.NewReader("second"))
	require.NoError(t, err)

	_, gotETag, err := b.CompleteMultipartUpload(ctx, "mp", "pv/complete.bin", uploadID,
		&gofakes3.CompleteMultipartUploadRequest{Parts: []gofakes3.CompletedPart{
			{PartNumber: 1, ETag: etag1},
			{PartNumber: 2, ETag: etag2},
		}})
	require.NoError(t, err, "the request body hash must not apply to the assembled object")
	require.NotEmpty(t, gotETag)

	obj, err := b.GetObject(ctx, "mp", "pv/complete.bin", nil)
	require.NoError(t, err)
	data, err := io.ReadAll(obj.Contents)
	require.NoError(t, err)
	assert.Equal(t, "first-second", string(data))
}
