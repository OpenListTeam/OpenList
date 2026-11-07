package s3

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/gofakes3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A credential setting that is present but unusable must never read as "no
// credentials configured": the gateway answers `?cors` before bucketAuthHandler
// runs, so an unchecked misconfiguration there let an anonymous caller replace
// or delete a bucket's CORS policy while the object path answered 503.
func TestS3CORSAPIAuthorized_MisconfiguredRefuses(t *testing.T) {
	for name, broken := range map[string]string{
		"broken JSON":     `[{"access_key_id":`,
		"rule without sk": `[{"access_key_id":"key1","buckets":["b"]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			setTestSetting(conf.S3BucketAuth, broken)
			setTestSetting(conf.S3AccessKeyId, "")
			setTestSetting(conf.S3SecretAccessKey, "")
			t.Cleanup(func() {
				setTestSetting(conf.S3BucketAuth, "")
			})

			for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
				req := httptest.NewRequest(method, "http://s3.example/b?cors", nil)
				status, code, _ := s3CORSAPIAuthorized(req, "b", method != http.MethodGet)
				assert.Equal(t, http.StatusServiceUnavailable, status, method)
				assert.Equal(t, "ServiceUnavailable", code, method)
			}
		})
	}
}

// The direct-transfer redirect runs in front of bucketAuthHandler as well, so a
// misconfigured setting must stop it from handing out storage URLs.
func TestS3RequestAuthorized_MisconfiguredRefuses(t *testing.T) {
	setTestSetting(conf.S3BucketAuth, `[{"access_key_id":`)
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	t.Cleanup(func() { setTestSetting(conf.S3BucketAuth, "") })

	ring := &keyRing{keys: map[string]string{}, lookup: resolveAuthPairsQuiet}
	assert.False(t, s3RequestAuthorized(httptest.NewRequest(http.MethodGet, "http://s3.example/b/k", nil), ring))
}

// A misspelt read-only flag would be dropped by encoding/json without a word and
// leave the key writable, so every plausible spelling has to be honoured.
func TestS3BucketAuthRule_ReadOnlySpellings(t *testing.T) {
	for _, raw := range []string{
		`{"access_key_id":"k","secret_access_key":"s","buckets":["b"],"readonly":true}`,
		`{"access_key_id":"k","secret_access_key":"s","buckets":["b"],"read_only":true}`,
		`{"access_key_id":"k","secret_access_key":"s","buckets":["b"],"readOnly":true}`,
	} {
		var rule S3BucketAuthRule
		require.NoError(t, rule.UnmarshalJSON([]byte(raw)))
		assert.True(t, rule.ReadOnly, raw)
	}

	var rule S3BucketAuthRule
	require.NoError(t, rule.UnmarshalJSON([]byte(
		`{"access_key_id":"k","secret_access_key":"s","buckets":["b"],"readonly":false}`)))
	assert.False(t, rule.ReadOnly)
}

// Two rows for one access key are merged, and the read-only flag is resolved per
// bucket: taking it from either row made one bucket's setting apply to all of
// them (a key marked read-only on "a" silently lost writes on "b").
func TestGetS3BucketAuthRules_DuplicateKeyReadOnlyPerBucket(t *testing.T) {
	setTestSetting(conf.S3BucketAuth, `[
		{"access_key_id":"key1","secret_access_key":"s1","buckets":["a"],"readonly":true},
		{"access_key_id":"key1","secret_access_key":"s1","buckets":["b"],"readonly":false}
	]`)
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	t.Cleanup(func() { setTestSetting(conf.S3BucketAuth, "") })

	rules, misconfigured := loadS3BucketAuthRules()
	require.False(t, misconfigured)
	require.Len(t, rules, 1)
	// Not entirely read-only any more, but "a" keeps its read-only setting.
	assert.False(t, rules[0].ReadOnly)
	assert.ElementsMatch(t, []string{"a", "b"}, rules[0].Buckets)
	assert.False(t, rules[0].CanWriteBucket("a"), "the read-only row must keep its bucket read-only")
	assert.True(t, rules[0].CanWriteBucket("b"), "the writable row must keep its bucket writable")
}

// gofakes3 verifies no signature at all while its key map is empty, so replacing
// the whole key set must add the new key before removing the old one.
func TestKeyRingSync_AddsBeforeDeleting(t *testing.T) {
	setTestSetting(conf.S3BucketAuth, `[{"access_key_id":"new","secret_access_key":"s2","buckets":["*"]}]`)
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	t.Cleanup(func() { setTestSetting(conf.S3BucketAuth, "") })

	ring := &keyRing{
		keys:   map[string]string{"old": "s1"},
		lookup: resolveAuthPairsQuiet,
		raw:    authFingerprint{"stale", "", ""},
	}
	sink := &recordingSink{}
	ring.bind(sink)

	assert.True(t, ring.sync())
	assert.Equal(t, []string{"add", "del"}, sink.calls)
	assert.Equal(t, map[string]string{"new": "s2"}, ring.snapshot())
}

// The virtual filesystem behind a bucket turns "\" into "/" before cleaning, so
// a key carrying a backslash passed the containment check and then resolved
// outside its bucket on read, write, delete and listing alike.
func TestCleanObjectKey_RejectsBackslash(t *testing.T) {
	for _, key := range []string{
		`..\..\other\secret.txt`,
		`a\b.txt`,
		`sub/..\..\victim.txt`,
	} {
		assert.ErrorIs(t, cleanObjectKey(key), errObjectKeyUnwritable, key)
	}
	// Ordinary keys keep working.
	for _, key := range []string{"a.txt", "dir/a.txt", "archive.tar.gz", "a//b"} {
		assert.NoError(t, cleanObjectKey(key), key)
	}
}

func TestObjectFSPath_RejectsBackslash(t *testing.T) {
	_, err := objectFSPath("/bucketA", `..\..\bucketB\secret.txt`)
	assert.ErrorIs(t, err, errObjectKeyUnwritable)
}

func TestObjectKeyGuard_RejectsBackslashOverHTTP(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the guard must not pass a key that escapes its bucket")
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://s3.example/bucketA/..%5C..%5CbucketB%5Csecret.txt", nil)
	objectKeyGuard(next).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "InvalidArgument")
}

// max-keys=0 asks for an empty page; reporting truncation without a marker made
// clients that loop on IsTruncated re-issue the same request forever.
func TestObjectPage_ZeroMaxKeysIsNotTruncated(t *testing.T) {
	p := newObjectPage(gofakes3.ListBucketPage{MaxKeys: 0})
	assert.False(t, p.addContent(&gofakes3.Content{Key: "a.txt"}))
	assert.Empty(t, p.result.Contents)
	assert.False(t, p.finish().IsTruncated)
	assert.Empty(t, p.finish().NextMarker)
}

// A rule allowing "*" must short-circuit header matching, and a requested header
// list is bounded, so an anonymous preflight cannot be made quadratic.
func TestS3CORSRule_AllowsHeadersWildcardAndCaps(t *testing.T) {
	rule := s3CORSRule{AllowedHeaders: []string{"*"}}
	assert.True(t, rule.allowsHeaders([]string{"x-a", "x-b", "x-c"}))

	long := make([]string, s3CORSMaxRequestHeaders*4)
	for i := range long {
		long[i] = "x-header-" + strconv.Itoa(i)
	}
	assert.LessOrEqual(t, len(splitHeaderList(strings.Join(long, ","))), s3CORSMaxRequestHeaders)
	assert.Error(t, checkS3CORSListBounds(make([]string, s3CORSMaxListEntries+1)))
	assert.Error(t, checkS3CORSListBounds([]string{strings.Repeat("a", s3CORSMaxEntryLength+1)}))
	assert.NoError(t, checkS3CORSListBounds([]string{"x-amz-meta-a", "*"}))
}

// `?cors` on the service root names no bucket; answering it with an empty 200
// hid the mistake.
func TestHandleS3CORSConfiguration_RequiresBucket(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://s3.example/?cors", nil)
	assert.True(t, handleS3CORSConfiguration(rec, req))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// A partial response must not carry the whole-object checksum: the SDKs
// validate it and fail with a checksum error on every ranged read.
func TestStripRangeChecksums_PartialResponse(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amz-Checksum-Crc32", "o6a/Qw==")
		w.Header().Set("ETag", `"abc"`)
		if r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusPartialContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// Range: the checksum goes, everything else stays.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://s3.example/bucket/key", nil)
	req.Header.Set("Range", "bytes=0-0")
	stripRangeChecksums(next).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Empty(t, rec.Header().Get("X-Amz-Checksum-Crc32"))
	assert.Equal(t, `"abc"`, rec.Header().Get("ETag"))

	// No range: the checksum is served as before.
	rec = httptest.NewRecorder()
	stripRangeChecksums(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s3.example/bucket/key", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "o6a/Qw==", rec.Header().Get("X-Amz-Checksum-Crc32"))
}

// A conditional answer carries no body, so the whole-object checksum has to go
// with it: botocore wraps such a body in a StreamingChecksumBody and then parses
// it as an XML error document, which raised TypeError instead of reporting the
// 304 to the caller.
func TestStripRangeChecksumsOnBodylessAnswers(t *testing.T) {
	for _, status := range []int{
		http.StatusNotModified,
		http.StatusPreconditionFailed,
		http.StatusNotFound,
		http.StatusRequestedRangeNotSatisfiable,
	} {
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Amz-Checksum-Crc32", "DUoRhQ==")
			w.Header().Set("X-Amz-Checksum-Type", "FULL_OBJECT")
			w.WriteHeader(status)
		})
		rec := httptest.NewRecorder()
		stripRangeChecksums(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s3.example/bucket/key", nil))
		assert.Equal(t, status, rec.Code)
		assert.Empty(t, rec.Header().Get("X-Amz-Checksum-Crc32"))
		assert.Empty(t, rec.Header().Get("X-Amz-Checksum-Type"))
	}
}

// A symbolic link inside the storage tree must not become a way out of the
// bucket, while a link that stays inside keeps working.
func TestResolvesInside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	assert.NoError(t, os.MkdirAll(filepath.Join(root, "bucketA", "dir"), 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644))
	assert.NoError(t, os.Symlink(outside, filepath.Join(root, "bucketA", "escape")))
	assert.NoError(t, os.Symlink(filepath.Join(root, "bucketA", "dir"), filepath.Join(root, "bucketA", "inner")))
	assert.NoError(t, os.Symlink("/etc/passwd", filepath.Join(root, "bucketA", "passwd")))

	// A plain key, an existing directory and a key that does not exist yet are
	// all inside.
	assert.NoError(t, resolvesInside(root, filepath.Join(root, "bucketA", "dir", "obj.txt"))) //nolint:gosec // test-local path
	assert.NoError(t, resolvesInside(root, filepath.Join(root, "bucketA", "missing", "obj.txt")))
	assert.NoError(t, resolvesInside(root, root))
	// A link that stays inside the mount is fine.
	assert.NoError(t, resolvesInside(root, filepath.Join(root, "bucketA", "inner", "obj.txt")))
	// A link to a directory outside the mount, and one to a file outside it, are
	// not.
	assert.ErrorIs(t, resolvesInside(root, filepath.Join(root, "bucketA", "escape", "secret.txt")), errObjectKeySymlink)
	assert.ErrorIs(t, resolvesInside(root, filepath.Join(root, "bucketA", "escape", "not-there-yet")), errObjectKeySymlink)
	assert.ErrorIs(t, resolvesInside(root, filepath.Join(root, "bucketA", "passwd")), errObjectKeySymlink)
}

// Subresources this gateway does not implement must say so instead of falling
// through to the listing handler and answering 200 with a bucket listing.
func TestUnsupportedSubresources(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := unsupportedSubresources(next)

	for _, sub := range []string{"acl", "tagging", "lifecycle", "policy", "logging", "encryption", "object-lock", "replication", "notification"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://s3.example/bucket?"+sub, nil)
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotImplemented, rec.Code, sub)
		assert.Contains(t, rec.Body.String(), "NotImplemented", sub)
	}

	// A bucket-level ?uploads listing is not implemented either, but the object
	// handlers must keep working for everything else.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s3.example/bucket?uploads", nil))
	assert.Equal(t, http.StatusNotImplemented, rec.Code)

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s3.example/bucket/key", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// List parameters that S3 rejects must not be silently reinterpreted.
func TestValidateListQuery(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := validateListQuery(next)

	cases := map[string]int{
		"/bucket?list-type=2&max-keys=-5":  http.StatusBadRequest,
		"/bucket?list-type=2&max-keys=abc": http.StatusBadRequest,
		"/bucket?list-type=3":              http.StatusBadRequest,
		"/bucket?list-type=2&max-keys=10":  http.StatusOK,
		"/bucket?list-type=1":              http.StatusOK,
		"/bucket?max-keys=0":               http.StatusOK,
		"/bucket":                          http.StatusOK,
	}
	for target, want := range cases {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s3.example"+target, nil))
		assert.Equal(t, want, rec.Code, target)
	}
}

// S3 renders a space in an encoded listing as %20; gofakes3 renders it as "+",
// which percent-decoding clients keep literally.
func TestFixPercentEncodedListing(t *testing.T) {
	in := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult><Name>bucket</Name><Prefix>e</Prefix><KeyCount>2</KeyCount>
<Contents><Key>e+</Key><Size>1</Size></Contents>
<Contents><Key>dir/a%2Bb</Key><Size>1</Size></Contents>
<Contents><Key>plain%2Bplus.txt</Key><Size>1</Size></Contents>
<CommonPrefixes><Prefix>my+dir/</Prefix></CommonPrefixes>
</ListBucketResult>`)
	out := string(fixPercentEncodedListing(in))
	assert.Contains(t, out, "<Key>e%20</Key>")
	assert.Contains(t, out, "<Key>dir/a%2Bb</Key>", "a literal plus is escaped upstream as %2B")
	assert.Contains(t, out, "<Key>plain%2Bplus.txt</Key>")
	assert.Contains(t, out, "<Prefix>my%20dir/</Prefix>")
	assert.NotContains(t, out, "<Key>e+</Key>")
}

// A Content-MD5 mismatch must stay a client error: returning the driver's
// wrapper untouched made gofakes3 answer InternalError, and an SDK retries 5xx.
func TestAsS3WriteErrorBadDigest(t *testing.T) {
	mapped := asS3WriteError(fmt.Errorf("copy body: %w", gofakes3.ErrBadDigest))
	code, ok := mapped.(interface{ ErrorCode() gofakes3.ErrorCode })
	if ok {
		assert.Equal(t, gofakes3.ErrBadDigest, code.ErrorCode())
		return
	}
	assert.Contains(t, mapped.Error(), "BadDigest")
}

// `partNumber` on a read must not be answered with the whole object: a client
// asking for part 2 would receive part 1's bytes where it expects part 2 and
// assemble a corrupt file. Part boundaries are not recorded, so the gateway says
// so instead of guessing.
func TestPartNumberReadIsRefused(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/bucket/object.bin?partNumber=2", nil)
		called := false
		partNumberGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })).
			ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotImplemented, rec.Code, method)
		assert.False(t, called, "the request must not reach the object handler")
		assert.Contains(t, rec.Body.String(), "NotImplemented")
	}

	// Without the parameter nothing changes.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/bucket/object.bin", nil)
	partNumberGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })).
		ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
