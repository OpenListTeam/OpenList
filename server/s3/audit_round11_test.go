package s3

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenListTeam/gofakes3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A body that fails its declared hash used to be committed straight over the
// key: the driver had already written it, and only two of the many error paths
// removed it. The upload now lands in a temporary, so the previous object must
// still be there, byte for byte.
func TestRejectedUploadLeavesTheExistingObjectIntact(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	_, err := b.PutObject(ctxDeclaringPayload(ctx, "original"), "mp", "keep.bin", nil,
		strings.NewReader("original"), int64(len("original")))
	require.NoError(t, err)

	_, err = b.PutObject(ctxDeclaringPayload(ctx, "not-the-body"), "mp", "keep.bin", nil,
		strings.NewReader("substituted"), int64(len("substituted")))
	require.Error(t, err)

	obj, err := b.GetObject(ctx, "mp", "keep.bin", nil)
	require.NoError(t, err)
	body := make([]byte, 64)
	n, _ := obj.Contents.Read(body)
	require.Equal(t, "original", string(body[:n]))
}

// The same rule for a key that did not exist: a rejected upload must not publish
// a partial or unverified object.
func TestRejectedUploadPublishesNothing(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	_, err := b.PutObject(ctxDeclaringPayload(ctx, "not-the-body"), "mp", "fresh.bin", nil,
		strings.NewReader("data"), int64(len("data")))
	require.Error(t, err)

	if _, err := b.HeadObject(ctx, "mp", "fresh.bin"); err == nil {
		t.Fatal("a rejected upload published an object")
	}
}

// A checksum the client declares is the only integrity value it can rely on; it
// used to be stored as decoration without ever being compared.
func TestClientChecksumIsVerifiedAndKept(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	good := base64.StdEncoding.EncodeToString(crc32.NewIEEE().Sum([]byte("payload")))
	if sum := crc32.ChecksumIEEE([]byte("payload")); true {
		good = base64.StdEncoding.EncodeToString([]byte{
			byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum),
		})
	}

	_, err := b.PutObject(ctx, "mp", "ck/ok.bin", map[string]string{"x-amz-checksum-crc32": good},
		strings.NewReader("payload"), int64(len("payload")))
	require.NoError(t, err)

	_, err = b.PutObject(ctx, "mp", "ck/bad.bin", map[string]string{"x-amz-checksum-crc32": good},
		strings.NewReader("different"), int64(len("different")))
	require.Error(t, err, "a body that contradicts the declared checksum must be refused")
}

// A framed body can decode to fewer bytes than the client announced; the
// truncation used to be stored as if it were the whole object.
func TestFramedBodyShorterThanAnnouncedIsRefused(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.WithValue(context.Background(), decodedLengthKey{}, int64(9))

	_, err := b.PutObject(ctx, "mp", "short.bin", nil, strings.NewReader("01234"), 5)
	require.Error(t, err)
	assert.Equal(t, gofakes3.ErrIncompleteBody, s3ErrorCode(err))
}

// gofakes3 de-chunks exactly two header spellings and silently treats every
// other streaming form as a plain body, which stores the chunk framing as the
// object. Unknown forms are refused instead.
func TestUnsupportedStreamingPayloadFormIsRefused(t *testing.T) {
	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })

	// A value that is not a streaming marker at all ("something-else") is left
	// alone: gofakes3 treats an unknown x-amz-content-sha256 as UNSIGNED-PAYLOAD,
	// which stores the body as sent. Only streaming-looking forms are dangerous.
	for _, form := range []string{
		"STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD",
		"streaming-aws4-hmac-sha256-payload-something-else",
	} {
		reached = false
		r := httptest.NewRequest(http.MethodPut, "/mp/framed.bin", strings.NewReader("x"))
		r.Header.Set("X-Amz-Content-Sha256", form)
		w := httptest.NewRecorder()
		contentSha256Guard(next).ServeHTTP(w, r)

		require.False(t, reached, "the guard let %q through to the storage", form)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "NotImplemented")
	}
}

// The spellings gofakes3 understands are normalised, so a client that writes the
// header in lowercase is not silently corrupted either.
func TestKnownStreamingFormIsNormalised(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Amz-Content-Sha256")
	})
	r := httptest.NewRequest(http.MethodPut, "/mp/framed.bin", strings.NewReader("x"))
	r.Header.Set("X-Amz-Content-Sha256", "streaming-unsigned-payload-trailer")
	r.Header.Set("X-Amz-Decoded-Content-Length", "4")
	contentSha256Guard(next).ServeHTTP(httptest.NewRecorder(), r)

	assert.Equal(t, "STREAMING-UNSIGNED-PAYLOAD-TRAILER", seen)
}

// "aws-chunked" frames the request, it is not a property of the stored object.
// Replaying it made a GET advertise an encoding the bytes do not have.
func TestAWSChunkedIsNotStoredAsContentEncoding(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	_, err := b.PutObject(ctx, "mp", "ce/framed.bin", map[string]string{
		"Content-Encoding": "gzip, aws-chunked",
	}, strings.NewReader("data"), int64(len("data")))
	require.NoError(t, err)

	obj, err := b.HeadObject(ctx, "mp", "ce/framed.bin")
	require.NoError(t, err)
	assert.Equal(t, "gzip", obj.Metadata["Content-Encoding"])
}

// Every read path answers the ETag of the content: conditional writes, cache
// revalidation and client-side change detection all depend on it, and an empty
// ETag made "If-Match" fail even for the current version.
func TestETagIsAnsweredOnReadsAndListings(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	content := "etag-body"
	_, err := b.PutObject(ctx, "mp", "etag/a.bin", nil, strings.NewReader(content), int64(len(content)))
	require.NoError(t, err)

	sum := md5.Sum([]byte(content))
	want := `"` + hex.EncodeToString(sum[:]) + `"`

	head, err := b.HeadObject(ctx, "mp", "etag/a.bin")
	require.NoError(t, err)
	assert.Equal(t, want, head.ETag)

	obj, err := b.GetObject(ctx, "mp", "etag/a.bin", nil)
	require.NoError(t, err)
	assert.Equal(t, want, obj.ETag)
	require.NoError(t, obj.Contents.Close())

	list, err := b.ListBucket(ctx, "mp", &gofakes3.Prefix{Prefix: "etag/"}, gofakes3.ListBucketPage{MaxKeys: 100})
	require.NoError(t, err)
	require.Len(t, list.Contents, 1)
	assert.Equal(t, want, list.Contents[0].ETag, "a listing entry without its ETag defeats client-side change detection")

	copied, err := b.CopyObject(ctx, "mp", "etag/a.bin", "mp", "etag/b.bin", map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, want, copied.ETag)
}

// A key that ends in "/" names a directory here, which cannot hold content. The
// body used to be dropped while the write still answered 200.
func TestDirectoryKeyWithBodyIsRefused(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	_, err := b.PutObject(ctx, "mp", "folder/", nil, strings.NewReader("body"), int64(len("body")))
	require.Error(t, err)
	assert.Equal(t, gofakes3.ErrInvalidArgument, s3ErrorCode(err))

	// The empty marker itself stays allowed: clients create it on purpose.
	_, err = b.PutObject(ctx, "mp", "folder/", nil, strings.NewReader(""), 0)
	require.NoError(t, err)
}

// Uploads stream into a temporary beside the key. A client that could address
// one would read another request's half-written bytes, or overwrite them.
func TestReservedTemporaryKeyIsRefused(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/mp/"+s3TempKeyPrefix+"deadbeef", nil)
	err := checkRequestKey(r)
	require.Error(t, err)

	r = httptest.NewRequest(http.MethodGet, "/mp/ordinary.bin", nil)
	require.NoError(t, checkRequestKey(r))
}

// A key cannot become an object while it still names a folder that holds
// objects: the staged upload is renamed over the key and that rename fails with
// a bare 500, which clients read as a server fault rather than a conflict.
func TestPutOverNonEmptyFolderIsRefused(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	_, err := b.PutObject(ctx, "mp", "conflict/child.txt", nil, strings.NewReader("child"), int64(len("child")))
	require.NoError(t, err)

	_, err = b.PutObject(ctx, "mp", "conflict", nil, strings.NewReader("object"), int64(len("object")))
	require.Error(t, err)
	assert.Equal(t, gofakes3.ErrInvalidArgument, s3ErrorCode(err))

	// The child it would have replaced is untouched.
	obj, err := b.GetObject(ctx, "mp", "conflict/child.txt", nil)
	require.NoError(t, err)
	content, err := io.ReadAll(obj.Contents)
	require.NoError(t, err)
	require.NoError(t, obj.Contents.Close())
	assert.Equal(t, "child", string(content))
}

// Deleting the last child leaves an empty folder behind. That folder holds
// nothing, so the key can take its place instead of blocking forever.
func TestPutOverEmptyFolderTakesItsPlace(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	_, err := b.PutObject(ctx, "mp", "takeover/child.txt", nil, strings.NewReader("child"), int64(len("child")))
	require.NoError(t, err)
	_, err = b.DeleteObject(ctx, "mp", "takeover/child.txt")
	require.NoError(t, err)

	_, err = b.PutObject(ctx, "mp", "takeover", nil, strings.NewReader("object"), int64(len("object")))
	require.NoError(t, err)

	obj, err := b.GetObject(ctx, "mp", "takeover", nil)
	require.NoError(t, err)
	content, err := io.ReadAll(obj.Contents)
	require.NoError(t, err)
	require.NoError(t, obj.Contents.Close())
	assert.Equal(t, "object", string(content))
}

// A delete of a key that cannot name an object (too long for the filesystem, or
// carrying a blank/step segment) must succeed like S3's idempotent delete. It
// used to surface the driver's ENAMETOOLONG as a 500.
func TestDeleteOfImpossibleKeySucceeds(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	long := strings.Repeat("x", 300)
	_, err := b.DeleteObject(ctx, "mp", long)
	require.NoError(t, err)

	// A key the gateway cannot represent at all is still refused: saying "not
	// valid" is more useful than pretending the delete worked.
	_, err = b.DeleteObject(ctx, "mp", "a/../b.txt")
	require.Error(t, err)
	assert.Equal(t, gofakes3.ErrInvalidArgument, s3ErrorCode(err))
}

// Storages that cannot rename (many cloud drivers) must keep working: the
// staged-then-renamed upload turned every PUT on them into "not implemented".
func TestPutObjectWithoutRenameSupportStillStoresTheObject(t *testing.T) {
	old := s3RenameSupported
	s3RenameSupported = func(context.Context, string) bool { return false }
	defer func() { s3RenameSupported = old }()

	b, root := setupMultipartBackend(t)
	ctx := context.Background()
	_, err := b.PutObject(ctx, "mp", "nr/file.txt",
		map[string]string{"Content-Type": "text/plain"}, strings.NewReader("hello"), 5)
	require.NoError(t, err)

	obj, err := b.GetObject(ctx, "mp", "nr/file.txt", nil)
	require.NoError(t, err)
	body, err := io.ReadAll(obj.Contents)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(body))

	// Nothing temporary may be left next to it.
	entries, err := os.ReadDir(filepath.Join(root, "nr"))
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{"file.txt"}, names)
}

// A failed direct write must not leave partial content advertised as the key.
func TestFailedDirectPutLeavesNoObject(t *testing.T) {
	old := s3RenameSupported
	s3RenameSupported = func(context.Context, string) bool { return false }
	defer func() { s3RenameSupported = old }()

	b, _ := setupMultipartBackend(t)
	ctx := context.Background()
	// Declares 10 bytes, delivers 5: the write reaches the driver, then the
	// length check rejects it.
	_, err := b.PutObject(ctx, "mp", "nr/short.txt", nil, strings.NewReader("hello"), 10)
	require.Error(t, err)

	_, err = b.GetObject(ctx, "mp", "nr/short.txt", nil)
	require.Error(t, err, "the rejected upload must not stay readable")
}

// The staging probe must say "yes" for a storage that can rename, otherwise
// every upload on the common local mount would take the non-atomic path.
func TestRenameSupportProbeSeesLocalDriver(t *testing.T) {
	setupMultipartBackend(t)
	mount := "/" + sanitizeTestName(t.Name())
	assert.True(t, s3RenameSupported(context.Background(), mount),
		"the Local driver implements rename, so uploads there stay staged")
}
