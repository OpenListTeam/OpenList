package s3

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuditRound42_MultiTenantWildcardIsolation tests multi-tenant bucket auth
// with prefix wildcards, cross-tenant isolation, and read-only enforcement.
func TestAuditRound42_MultiTenantWildcardIsolation(t *testing.T) {
	rawRules := `[
		{"access_key_id":"ak-tenant-a","secret_access_key":"sk-tenant-a","buckets":["tenant-a-*"],"readonly":false},
		{"access_key_id":"ak-tenant-b","secret_access_key":"sk-tenant-b","buckets":["tenant-b-*"],"readonly":false},
		{"access_key_id":"ak-audit","secret_access_key":"sk-audit","buckets":["*"],"readonly":true}
	]`
	setTestSetting(conf.S3BucketAuth, rawRules)
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	defer func() {
		setTestSetting(conf.S3BucketAuth, "")
	}()

	rules, misconfigured := loadS3BucketAuthRules()
	require.False(t, misconfigured)
	require.Len(t, rules, 3)

	var ruleA, ruleB, ruleAudit *S3BucketAuthRule
	for i := range rules {
		switch rules[i].AccessKey {
		case "ak-tenant-a":
			ruleA = &rules[i]
		case "ak-tenant-b":
			ruleB = &rules[i]
		case "ak-audit":
			ruleAudit = &rules[i]
		}
	}
	require.NotNil(t, ruleA)
	require.NotNil(t, ruleB)
	require.NotNil(t, ruleAudit)

	// 1. Tenant A wildcard matches tenant-a-1, tenant-a-prod, but NOT tenant-b-1 or audit-log
	assert.True(t, ruleA.MatchesBucket("tenant-a-1"))
	assert.True(t, ruleA.MatchesBucket("tenant-a-prod"))
	assert.False(t, ruleA.MatchesBucket("tenant-b-1"))
	assert.False(t, ruleA.MatchesBucket("tenant-other"))
	assert.True(t, ruleA.CanWriteBucket("tenant-a-1"))

	// 2. Tenant B wildcard matches tenant-b-1, but NOT tenant-a-1
	assert.True(t, ruleB.MatchesBucket("tenant-b-1"))
	assert.False(t, ruleB.MatchesBucket("tenant-a-1"))
	assert.True(t, ruleB.CanWriteBucket("tenant-b-1"))

	// 3. Audit wildcard matches everything but CANNOT write
	assert.True(t, ruleAudit.MatchesBucket("tenant-a-1"))
	assert.True(t, ruleAudit.MatchesBucket("tenant-b-1"))
	assert.False(t, ruleAudit.CanWriteBucket("tenant-a-1"))
	assert.False(t, ruleAudit.CanWriteBucket("tenant-b-1"))

	// 4. Test bucketAuthHandler middleware rejects cross-tenant access and write violation
	handler := bucketAuthHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}), http.NotFoundHandler())

	// Tenant A accesses tenant-a-1 -> 200 OK
	req1 := httptest.NewRequest("GET", "/tenant-a-1/file.txt", nil)
	req1.Header.Set("Authorization", "AWS ak-tenant-a:dummy")
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Tenant A attempts access to tenant-b-1 -> 403 Forbidden
	req2 := httptest.NewRequest("GET", "/tenant-b-1/file.txt", nil)
	req2.Header.Set("Authorization", "AWS ak-tenant-a:dummy")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusForbidden, w2.Code)
	assert.Contains(t, w2.Body.String(), "AccessDenied")

	// Audit key attempts write (PUT) to tenant-a-1 -> 403 Forbidden
	req3 := httptest.NewRequest("PUT", "/tenant-a-1/file.txt", nil)
	req3.Header.Set("Authorization", "AWS ak-audit:dummy")
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)
	assert.Equal(t, http.StatusForbidden, w3.Code)
	assert.Contains(t, w3.Body.String(), "Write operations not permitted")
}

// TestAuditRound42_ObjectPathSecurity verifies strict path traversal prevention,
// backslash blocking, NUL byte detection, and double-slash handling.
func TestAuditRound42_ObjectPathSecurity(t *testing.T) {
	// 1. Invalid keys that must fail cleanObjectKey
	traversalKeys := []string{
		"..",
		".",
		"../secret",
		"dir/..",
		"dir/../secret",
		"dir/./secret",
		"foo/bar/../../etc/passwd",
		"back\\slash",
		"null\x00byte",
	}
	for _, key := range traversalKeys {
		err := cleanObjectKey(key)
		assert.Error(t, err, "key %q should be rejected by cleanObjectKey", key)
	}

	// 2. Empty segments ("//") must fail cleanObjectPathKey
	emptySegKeys := []string{
		"a//b",
		"//leading",
		"trailing//",
		"a///b",
	}
	for _, key := range emptySegKeys {
		err := cleanObjectPathKey(key)
		assert.ErrorIs(t, err, errObjectKeyEmptySeg, "key %q should fail with errObjectKeyEmptySeg", key)
	}

	// 3. Valid keys should pass
	validKeys := []string{
		"plain.txt",
		"folder/file.json",
		"deep/nested/path/to/object.tar.gz",
		"dots.inside.name.ext",
		"unicode_中文_文件名.png",
	}
	for _, key := range validKeys {
		assert.NoError(t, cleanObjectKey(key), "valid key %q should pass cleanObjectKey", key)
		assert.NoError(t, cleanObjectPathKey(key), "valid key %q should pass cleanObjectPathKey", key)
	}

	// 4. objectFSPath confinement test
	bucketRoot := "/tmp/mock-bucket-root"
	safePath, err := objectFSPath(bucketRoot, "safe/doc.pdf")
	require.NoError(t, err)
	assert.Equal(t, path.Join(bucketRoot, "safe/doc.pdf"), safePath)

	// Path escape attempt
	_, err = objectFSPath(bucketRoot, "../escaped.txt")
	assert.Error(t, err)
}

// TestAuditRound42_MetadataSanitization verifies that transient/sensitive request headers
// are stripped while user metadata, checksums, and standard object headers are preserved.
func TestAuditRound42_MetadataSanitization(t *testing.T) {
	inputHeaders := map[string]string{
		// Sensitive / Transient headers that MUST be dropped
		"X-Amz-Security-Token": "temp-credential-token-should-not-persist",
		"X-Amz-Copy-Source":    "/source-bucket/source-object",
		"X-Amz-Date":           "20261030T120000Z",
		"X-Amz-Content-Sha256": "UNSIGNED-PAYLOAD",
		"Authorization":        "AWS4-HMAC-SHA256 ...",

		// Valid User Metadata that MUST be kept
		"x-amz-meta-author":      "Alice",
		"x-amz-meta-environment": "Production",

		// Object-level HTTP headers that MUST be kept
		"Content-Type":        "application/json",
		"Content-Disposition": "inline; filename=\"test.json\"",
		"Cache-Control":       "max-age=3600",

		// Framing headers that must be sanitized
		"Content-Encoding": "aws-chunked, gzip",

		// Whole object checksums that MUST be kept
		"x-amz-checksum-crc32": "abc123==",
	}

	clean := storableMetadata(inputHeaders)

	// Sensitive headers dropped
	assert.NotContains(t, clean, "X-Amz-Security-Token")
	assert.NotContains(t, clean, "x-amz-security-token")
	assert.NotContains(t, clean, "X-Amz-Copy-Source")
	assert.NotContains(t, clean, "x-amz-copy-source")
	assert.NotContains(t, clean, "X-Amz-Date")
	assert.NotContains(t, clean, "x-amz-date")
	assert.NotContains(t, clean, "X-Amz-Content-Sha256")
	assert.NotContains(t, clean, "Authorization")

	// User metadata kept (normalized to user metadata prefix)
	assert.Equal(t, "Alice", clean["x-amz-meta-author"])
	assert.Equal(t, "Production", clean["x-amz-meta-environment"])

	// Object headers kept
	assert.Equal(t, "application/json", clean["Content-Type"])
	assert.Equal(t, "inline; filename=\"test.json\"", clean["Content-Disposition"])
	assert.Equal(t, "max-age=3600", clean["Cache-Control"])

	// aws-chunked removed from Content-Encoding
	assert.Equal(t, "gzip", clean["Content-Encoding"])

	// Checksum kept
	assert.Equal(t, "abc123==", clean["x-amz-checksum-crc32"])
}

// TestAuditRound42_TieringAccessRecording verifies thread safety and concurrency of
// object access recording (used by tiering lifecycle policies).
func TestAuditRound42_TieringAccessRecording(t *testing.T) {
	resetMetadataTable(t)
	b := newBackend().(*s3Backend)
	name := "/tiering-test/access-hot.bin"

	// Store initial metadata
	ctx := context.Background()
	b.storeObjectMetadata(ctx, name, map[string]string{"x-amz-meta-initial": "yes"}, nil, "")

	// Concurrent access time updates
	var wg sync.WaitGroup
	workers := 10
	now := time.Now().UTC().Truncate(time.Second)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			recordRead(t, b, name, now)
		}(i)
	}
	wg.Wait()

	// Verify metadata remains intact and access timestamp is stored
	row := metadataRow(t, name)
	require.NotNil(t, row)
	require.NotNil(t, row.LastAccess)
	assert.True(t, row.LastAccess.Equal(now), "last access should match recorded timestamp")

	headers, ok := b.loadStoredMetadata(ctx, name)
	require.True(t, ok)
	assert.Equal(t, "yes", headers["x-amz-meta-initial"])
}
