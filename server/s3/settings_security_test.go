package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/gofakes3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// --- SEC-1: an incompletely configured gateway must fail closed ---------------

// A gateway with a secret but no access key used to look "unconfigured" to the
// request path, because that decision reads the key ring and the ring drops an
// empty access key. Open mode then let every anonymous request through.
func TestGateRefusesIncompleteCredentials(t *testing.T) {
	t.Cleanup(func() {
		setTestSetting(conf.S3BucketAuth, "")
		setTestSetting(conf.S3AccessKeyId, "")
		setTestSetting(conf.S3SecretAccessKey, "")
	})

	setTestSetting(conf.S3BucketAuth, "")
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "only-a-secret")
	rules, misconfigured := loadS3BucketAuthRules()
	assert.True(t, misconfigured, "a secret without an access key must not mean open mode")
	assert.Empty(t, rules)

	// The mirror image: an access key without a secret cannot sign anything.
	setTestSetting(conf.S3AccessKeyId, "only-an-access-key")
	setTestSetting(conf.S3SecretAccessKey, "")
	rules, misconfigured = loadS3BucketAuthRules()
	assert.True(t, misconfigured)
	assert.Empty(t, rules)

	// Nothing configured at all is still the documented open mode.
	setTestSetting(conf.S3AccessKeyId, "")
	rules, misconfigured = loadS3BucketAuthRules()
	assert.False(t, misconfigured)
	assert.Empty(t, rules)
}

// --- AUTH-07: access keys shorter than three characters are refused ----------

func TestShortAccessKeysAreDropped(t *testing.T) {
	t.Cleanup(func() {
		setTestSetting(conf.S3BucketAuth, "")
		setTestSetting(conf.S3AccessKeyId, "")
		setTestSetting(conf.S3SecretAccessKey, "")
	})
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")

	setTestSetting(conf.S3BucketAuth, `[{"access_key_id":"ab","secret_access_key":"s","buckets":["*"]}]`)
	rules, misconfigured := loadS3BucketAuthRules()
	assert.Empty(t, rules, "a two-character access key must not be accepted")
	assert.True(t, misconfigured, "dropping every rule must fail closed, not open")

	setTestSetting(conf.S3BucketAuth, `[{"access_key_id":"abc","secret_access_key":"s","buckets":["*"]}]`)
	rules, misconfigured = loadS3BucketAuthRules()
	assert.False(t, misconfigured)
	require.Len(t, rules, 1)
	assert.Equal(t, "abc", rules[0].AccessKey)
}

// --- AUTH-03: presigned links are bounded in both directions ------------------

func TestPresignedLifetimeGuard(t *testing.T) {
	call := func(query string) (int, bool) {
		called := false
		handler := presignedLifetimeGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/bucket/obj?"+query, nil))
		return recorder.Code, called
	}

	now := time.Now().UTC().Format("20060102T150405Z")
	code, called := call("X-Amz-Signature=abc&X-Amz-Date=" + now + "&X-Amz-Expires=3600")
	assert.True(t, called, "a normal presigned URL must pass")
	assert.Equal(t, http.StatusOK, code)

	code, called = call("X-Amz-Signature=abc&X-Amz-Date=" + now + "&X-Amz-Expires=9999999999")
	assert.False(t, called, "a decades-long presigned URL must be refused")
	assert.Equal(t, http.StatusForbidden, code)

	code, called = call("X-Amz-Signature=abc&X-Amz-Date=" + now + "&X-Amz-Expires=-1")
	assert.False(t, called)
	assert.Equal(t, http.StatusBadRequest, code)

	future := time.Now().UTC().Add(time.Hour).Format("20060102T150405Z")
	code, called = call("X-Amz-Signature=abc&X-Amz-Date=" + future + "&X-Amz-Expires=3600")
	assert.False(t, called, "a URL dated in the future must be refused")
	assert.Equal(t, http.StatusForbidden, code)

	// A signed header request is untouched by this guard.
	called = false
	handler := presignedLifetimeGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/bucket/obj", nil))
	assert.True(t, called)
}

// --- AUTH-08: the declared payload hash is actually compared -----------------

func TestContentSha256Guard(t *testing.T) {
	// The check streams: the handler runs, and reading the body to its end is
	// what produces the error that makes the storage layer discard the object.
	serve := func(body, declared string, signed bool) (int, bool, string, error) {
		called := false
		var seen string
		var readErr error
		handler := contentSha256Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if r.Body != nil {
				buf, err := io.ReadAll(r.Body)
				seen = string(buf)
				readErr = err
			}
		}))
		request := httptest.NewRequest(http.MethodPut, "/bucket/obj", strings.NewReader(body))
		if signed {
			request.Header.Set("X-Amz-Content-Sha256", declared)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code, called, seen, readErr
	}

	sum := sha256.Sum256([]byte("the bytes that were signed"))
	good := hex.EncodeToString(sum[:])

	code, called, seen, err := serve("the bytes that were signed", good, true)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "the bytes that were signed", seen)
	assert.NoError(t, err)

	// A substituted body must not be committed: the read fails at EOF, which is
	// what aborts the write.
	code, called, seen, err = serve("the bytes that arrived", good, true)
	assert.True(t, called, "the streaming guard cannot refuse before the handler runs")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "the bytes that arrived", seen)
	assert.ErrorIs(t, err, errPayloadHashMismatch)

	// A body larger than the old buffer limit is verified too - that was the
	// hole this replaced.
	bigBody := strings.Repeat("a", 9<<20)
	bigSum := sha256.Sum256([]byte(bigBody))
	code, _, _, err = serve(bigBody+"tail", hex.EncodeToString(bigSum[:]), true)
	assert.Equal(t, http.StatusOK, code)
	assert.ErrorIs(t, err, errPayloadHashMismatch, "bodies above the old 8 MiB cap must be verified")

	// UNSIGNED-PAYLOAD does not describe the body, so nothing is compared.
	code, called, _, err = serve("whatever", "UNSIGNED-PAYLOAD", true)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, code)
	assert.NoError(t, err)

	// An unsigned request is untouched.
	code, called, _, err = serve("whatever", "", false)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, code)
	assert.NoError(t, err)
}

// --- AUTH-02 / AUTH-05: copy source presence and signing --------------------

func TestCopySourceRefDetectsEmptyHeader(t *testing.T) {
	request := httptest.NewRequest(http.MethodPut, "/bucket/obj", nil)
	request.Header["X-Amz-Copy-Source"] = []string{""}

	bucket, key, present := copySourceRef(request)
	assert.True(t, present, "an empty copy-source header is still a copy request")
	assert.Empty(t, bucket)
	assert.Empty(t, key)

	request = httptest.NewRequest(http.MethodPut, "/bucket/obj", nil)
	request.Header.Set("X-Amz-Copy-Source", "/other/path/to/key")
	bucket, key, present = copySourceRef(request)
	assert.True(t, present)
	assert.Equal(t, "other", bucket)
	assert.Equal(t, "path/to/key", key)
}

func TestPresignedCopySourceMustBeSigned(t *testing.T) {
	presigned := httptest.NewRequest(http.MethodPut,
		"/bucket/obj?X-Amz-Signature=abc&X-Amz-SignedHeaders=host", nil)
	presigned.Header.Set("X-Amz-Copy-Source", "/secret/bucket/key")
	assert.False(t, copySourceIsSigned(presigned),
		"a presigned URL that did not sign the copy source must be refused")

	presigned = httptest.NewRequest(http.MethodPut,
		"/bucket/obj?X-Amz-Signature=abc&X-Amz-SignedHeaders=host%3Bx-amz-copy-source", nil)
	presigned.Header.Set("X-Amz-Copy-Source", "/secret/bucket/key")
	assert.True(t, copySourceIsSigned(presigned))

	// Header-signed requests already cover every header they send.
	headerSigned := httptest.NewRequest(http.MethodPut, "/bucket/obj", nil)
	headerSigned.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=key/20261029/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-copy-source, Signature=abc")
	headerSigned.Header.Set("X-Amz-Copy-Source", "/secret/bucket/key")
	assert.True(t, copySourceIsSigned(headerSigned))
}

// UploadPartCopy talks to the backend before gofakes3 verifies anything, so an
// unsigned request has to fall through to gofakes3 instead of copying.
func TestUploadPartCopyRefusesUnsignedRequest(t *testing.T) {
	called := false
	handler := uploadPartCopyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}), nil, newKeyRing(map[string]string{"known-access": "known-secret"}))

	request := httptest.NewRequest(http.MethodPut, "/bucket/obj?partNumber=1&uploadId=abc", nil)
	request.Header.Set("X-Amz-Copy-Source", "/source/bucket/key")
	request.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=known-access/20261029/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=forged")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	assert.True(t, called, "a request whose signature does not verify must be left to gofakes3")
}

// --- SEC-2: a partial save must not publish the master key ------------------

func TestSaveSettingItemKeepsSensitiveRowPrivate(t *testing.T) {
	require.NoError(t, db.SaveSettingItem(&model.SettingItem{
		Key:   conf.S3SecretAccessKey,
		Value: "master-secret",
		Type:  conf.TypeString,
		Group: model.S3,
		Flag:  model.PRIVATE,
	}))
	t.Cleanup(func() {
		require.NoError(t, db.SaveSettingItem(&model.SettingItem{
			Key:   conf.S3SecretAccessKey,
			Value: "",
			Type:  conf.TypeString,
			Group: model.S3,
			Flag:  model.PRIVATE,
		}))
		op.SettingCacheUpdate()
	})

	// What a partial admin payload looks like: only key and value.
	require.NoError(t, op.SaveSettingItem(&model.SettingItem{
		Key:   conf.S3SecretAccessKey,
		Value: "rotated-secret",
	}))

	stored, err := db.GetSettingItemByKey(conf.S3SecretAccessKey)
	require.NoError(t, err)
	assert.Equal(t, model.PRIVATE, stored.Flag, "a sensitive row must never become public")
	assert.Equal(t, model.S3, stored.Group, "the group a partial payload omitted must be kept")
	assert.Equal(t, conf.TypeString, stored.Type)
	assert.Equal(t, "rotated-secret", stored.Value)
}

// --- SEC-4: the settings API refuses documents that would break the gateway --

func TestSettingHooksRejectBrokenDocuments(t *testing.T) {
	hook := func(key, value string) error {
		_, err := op.HandleSettingItemHook(&model.SettingItem{Key: key, Value: value})
		return err
	}

	assert.Error(t, hook(conf.S3BucketAuth, "{not json"),
		"a broken s3_bucket_auth would drop every key")
	assert.Error(t, hook(conf.S3Buckets, "{not json"))
	assert.Error(t, hook(conf.S3BucketCors, "{not json"))
	assert.Error(t, hook(conf.S3Buckets, `[{"name":"root","path":"/"}]`),
		"a bucket pointing at the storage root exposes the whole mount")
	assert.Error(t, hook(conf.S3Buckets, `[{"name":"empty","path":""}]`))
	assert.Error(t, hook(conf.S3Buckets, `[{"name":"","path":"/x"}]`))

	assert.NoError(t, hook(conf.S3BucketAuth, ""))
	assert.NoError(t, hook(conf.S3Buckets, ""))
	assert.NoError(t, hook(conf.S3BucketCors, ""))
	assert.NoError(t, hook(conf.S3BucketAuth,
		`[{"access_key_id":"abcd","secret_access_key":"s","buckets":["*"]}]`))
	assert.NoError(t, hook(conf.S3Buckets, `[{"name":"vps","path":"/cold/vps"}]`))
}

// --- SEC-5: a bucket may not resolve to the storage root ---------------------

func TestBucketRootPathIsRefused(t *testing.T) {
	t.Cleanup(func() { setTestSetting(conf.S3Buckets, "") })

	setTestSetting(conf.S3Buckets, `[{"name":"root","path":"/"},{"name":"empty","path":""},{"name":"ok","path":"/cold/vps"}]`)
	buckets, err := getAndParseBuckets()
	require.NoError(t, err)

	names := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		names = append(names, bucket.Name)
	}
	assert.NotContains(t, names, "root")
	assert.NotContains(t, names, "empty")
	assert.Contains(t, names, "ok")
}

// --- SEC-7: a concurrent edit is reported instead of overwritten ------------

func TestCORSSaveDetectsLostUpdate(t *testing.T) {
	t.Cleanup(func() { setTestSetting(conf.S3BucketCors, "") })

	setTestSetting(conf.S3BucketCors, `{"bucket-a":[{"allowed_origins":["*"],"allowed_methods":["GET"]}]}`)

	// The admin edited the setting after this request read it.
	err := saveS3CORSSetting("a-different-document-than-stored", map[string][]s3CORSRule{
		"bucket-b": {{AllowedOrigins: []string{"*"}, AllowedMethods: []string{"GET"}}},
	})
	assert.ErrorIs(t, err, errS3CORSConfigurationChanged)
}

// --- SEC-8: the cached configuration is not handed out by reference ---------

func TestCORSConfigurationIsCloned(t *testing.T) {
	t.Cleanup(func() { setTestSetting(conf.S3BucketCors, "") })

	setTestSetting(conf.S3BucketCors,
		`{"bucket-a":[{"allowed_origins":["https://a.example"],"allowed_methods":["GET"]}]}`)

	first := getS3CORSConfiguration()
	require.Len(t, first["bucket-a"], 1)
	first["bucket-a"][0].AllowedOrigins[0] = "https://evil.example"
	first["bucket-b"] = []s3CORSRule{{AllowedOrigins: []string{"*"}, AllowedMethods: []string{"PUT"}}}

	second := getS3CORSConfiguration()
	require.Len(t, second["bucket-a"], 1)
	assert.Equal(t, "https://a.example", second["bucket-a"][0].AllowedOrigins[0],
		"mutating a returned policy must not change what the next request sees")
	assert.NotContains(t, second, "bucket-b")
}

// --- SEC-1 (second half): an unparsable document fails closed ---------------

func TestUnparsableAuthRulesFailClosed(t *testing.T) {
	t.Cleanup(func() {
		setTestSetting(conf.S3BucketAuth, "")
		setTestSetting(conf.S3AccessKeyId, "")
		setTestSetting(conf.S3SecretAccessKey, "")
	})
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	setTestSetting(conf.S3BucketAuth, "{not json")

	rules, misconfigured := loadS3BucketAuthRules()
	assert.True(t, misconfigured)
	assert.Empty(t, rules)
}

// --- interop: a key written as "<prefix>/" must be readable ---------------

func TestDirMarkerObjectAnswersTrailingSlashKeys(t *testing.T) {
	backend := &s3Backend{}
	dir := &model.Object{IsFolder: true, Modified: time.Now()}

	obj, ok := backend.dirMarkerObject("prefix/", dir, false)
	require.True(t, ok, "a PUT of \"prefix/\" claimed to create a key; the read must answer it")
	assert.Equal(t, int64(0), obj.Size)

	_, ok = backend.dirMarkerObject("prefix", dir, false)
	assert.False(t, ok, "a bare prefix is not an object and must stay NoSuchKey")

	file := &model.Object{}
	_, ok = backend.dirMarkerObject("prefix/", file, false)
	assert.False(t, ok, "only directories stand in for directory markers")
}

// The trailing slash is gone by the time the backend sees the key, so the guard
// has to remember it or a directory marker can never be read back.
func TestObjectKeyGuardMarksDirectoryMarkerRequests(t *testing.T) {
	var seen bool
	handler := objectKeyGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = isDirMarkerRequest(r.Context())
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/bucket/dir/", nil))
	assert.True(t, seen, "a key written as \"dir/\" is a directory marker")

	seen = true
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/bucket/dir/file.txt", nil))
	assert.False(t, seen, "an ordinary key is not a marker")

	seen = true
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/bucket/", nil))
	assert.False(t, seen, "the bucket root names no key")
}

// A DeleteObjects document above the documented 1000-key limit is accepted by
// the XML parser, so the gateway must refuse it instead of deleting thousands
// of keys in a request the caller believes failed.
func TestDeleteMultiRejectsMoreThanThousandKeys(t *testing.T) {
	backend := &s3Backend{}
	keys := make([]string, maxMultiDeleteKeys+1)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%04d", i)
	}

	result, err := backend.DeleteMulti(context.Background(), "any", keys...)
	require.Error(t, err, "1001 keys must not be deleted")
	resp, ok := err.(*gofakes3.ErrorResponse)
	require.True(t, ok, "the refusal has to carry an S3 error response, got %v", err)
	assert.Equal(t, gofakes3.ErrMalformedXML, resp.Code)
	assert.Empty(t, result.Deleted)
	assert.Empty(t, result.Error)
}

// The marker object's Last-Modified is labelled GMT, so it has to be formatter in
// UTC. Printing a local time next to a GMT label shifts every conditional request
// by the server's timezone offset.
func TestDirMarkerLastModifiedIsUTC(t *testing.T) {
	backend := &s3Backend{}
	zone := time.FixedZone("UTC+8", 8*60*60)
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, zone)
	dir := &model.Object{IsFolder: true, Modified: modified}

	obj, ok := backend.dirMarkerObject("prefix/", dir, true)
	require.True(t, ok)

	stamp := obj.Metadata["Last-Modified"]
	parsed, err := http.ParseTime(stamp)
	require.NoError(t, err, "Last-Modified %q is not an HTTP date", stamp)
	assert.True(t, parsed.Equal(modified.UTC()), "Last-Modified %q is not the object time in UTC (want %s)", stamp, modified.UTC().Format(time.RFC3339))
}

// --- D5/D9: names that would become wildcards, and one path for two buckets ---

func TestSettingHooksRejectWildcardBucketNames(t *testing.T) {
	hook := func(key, value string) error {
		_, err := op.HandleSettingItemHook(&model.SettingItem{Key: key, Value: value})
		return err
	}

	// s3_bucket_auth matches "*" and "prefix*" as wildcards, so a bucket called
	// "*" turns every per-bucket key into a key for every bucket.
	assert.Error(t, hook(conf.S3Buckets, `[{"name":"*","path":"/cold/vps"}]`))
	assert.Error(t, hook(conf.S3Buckets, `[{"name":"logs*","path":"/cold/vps"}]`))
	// Neither can be addressed by a request path.
	assert.Error(t, hook(conf.S3Buckets, `[{"name":"a/b","path":"/cold/vps"}]`))
	assert.Error(t, hook(conf.S3Buckets, `[{"name":"a b","path":"/cold/vps"}]`))
	// Two buckets on one directory: the read-only one is writable through the
	// other, so the read-only flag would be a lie.
	assert.Error(t, hook(conf.S3Buckets,
		`[{"name":"a","path":"/cold/vps"},{"name":"b","path":"/cold/vps/"}]`),
		"the same path twice must be refused, trailing slash included")

	// Everything the S3 rules allow still passes, including legacy shapes
	// (upper case, underscore) so an existing install can be saved again.
	assert.NoError(t, hook(conf.S3Buckets, `[{"name":"Logs.Bucket_1","path":"/cold/vps"}]`))
	assert.NoError(t, hook(conf.S3Buckets,
		`[{"name":"a","path":"/cold/vps"},{"name":"b","path":"/cold/vps/sub"}]`))
}

// --- D2: read-only belongs to the bucket, not to the access key --------------

func TestMergedRuleKeepsReadOnlyPerBucket(t *testing.T) {
	setTestSetting(conf.S3BucketAuth, `[
		{"access_key_id":"key1","secret_access_key":"s1","buckets":["ro"],"readonly":true},
		{"access_key_id":"key1","secret_access_key":"s1","buckets":["rw"]}
	]`)
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	t.Cleanup(func() { setTestSetting(conf.S3BucketAuth, "") })

	rules, misconfigured := loadS3BucketAuthRules()
	require.False(t, misconfigured)
	require.Len(t, rules, 1)
	assert.False(t, rules[0].CanWriteBucket("ro"),
		"the bucket marked read-only must stay read-only")
	assert.True(t, rules[0].CanWriteBucket("rw"),
		"the bucket marked writable must not inherit the other row's read-only flag")
}

// A wildcard row cannot be matched bucket by bucket, so the whole-rule flag is
// kept: "*" marked read-only must mean read-only everywhere.
func TestMergedRuleWithWildcardKeepsWholeRuleFlag(t *testing.T) {
	setTestSetting(conf.S3BucketAuth, `[
		{"access_key_id":"key2","secret_access_key":"s2","buckets":["*"],"readonly":true},
		{"access_key_id":"key2","secret_access_key":"s2","buckets":["rw"]}
	]`)
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	t.Cleanup(func() { setTestSetting(conf.S3BucketAuth, "") })

	rules, misconfigured := loadS3BucketAuthRules()
	require.False(t, misconfigured)
	require.Len(t, rules, 1)
	assert.True(t, rules[0].ReadOnly)
	assert.False(t, rules[0].CanWriteBucket("rw"))
}
