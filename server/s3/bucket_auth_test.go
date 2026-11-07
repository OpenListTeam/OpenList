package s3

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setTestSetting(key, val string) {
	op.Cache.SetSetting(key, &model.SettingItem{Key: key, Value: val})
}

func TestS3BucketAuthRule_MatchesBucket(t *testing.T) {
	wildcardRule := S3BucketAuthRule{
		AccessKey: "admin",
		Buckets:   []string{"*"},
	}
	assert.True(t, wildcardRule.MatchesBucket("any-bucket"))
	assert.False(t, wildcardRule.MatchesBucket(""))

	prefixRule := S3BucketAuthRule{
		AccessKey: "dev",
		Buckets:   []string{"logto", "test-*"},
	}
	assert.True(t, prefixRule.MatchesBucket("logto"))
	assert.True(t, prefixRule.MatchesBucket("test-dev"))
	assert.True(t, prefixRule.MatchesBucket("test-prod"))
	assert.False(t, prefixRule.MatchesBucket("other"))
	assert.False(t, prefixRule.MatchesBucket("tes"))

	readOnlyRule := S3BucketAuthRule{
		AccessKey: "reader",
		Buckets:   []string{"logto"},
		ReadOnly:  true,
	}
	assert.False(t, readOnlyRule.CanWrite())

	readWriteRule := S3BucketAuthRule{
		AccessKey: "writer",
		Buckets:   []string{"logto"},
		ReadOnly:  false,
	}
	assert.True(t, readWriteRule.CanWrite())
}

func TestExtractAccessKey(t *testing.T) {
	// 1. AWS V4 Header
	reqV4, _ := http.NewRequest("GET", "http://localhost:5246/mybucket/file.txt", nil)
	reqV4.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20261028/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=xxxx")
	assert.Equal(t, "AKIAIOSFODNN7EXAMPLE", extractAccessKey(reqV4))

	// 2. AWS V4 Presigned URL
	reqV4Query, _ := http.NewRequest("GET", "http://localhost:5246/mybucket/file.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=QUERY_AK_123%2F20261028%2Fus-east-1%2Fs3%2Faws4_request", nil)
	assert.Equal(t, "QUERY_AK_123", extractAccessKey(reqV4Query))

	// 3. AWS V2 Header
	reqV2, _ := http.NewRequest("GET", "http://localhost:5246/mybucket/file.txt", nil)
	reqV2.Header.Set("Authorization", "AWS V2_ACCESS_KEY:c2lnbmF0dXJl")
	assert.Equal(t, "V2_ACCESS_KEY", extractAccessKey(reqV2))

	// 4. AWS V2 Presigned URL
	reqV2Query, _ := http.NewRequest("GET", "http://localhost:5246/mybucket/file.txt?AWSAccessKeyId=V2_QUERY_KEY", nil)
	assert.Equal(t, "V2_QUERY_KEY", extractAccessKey(reqV2Query))

	// 5. Empty
	reqEmpty, _ := http.NewRequest("GET", "http://localhost:5246/mybucket/file.txt", nil)
	assert.Equal(t, "", extractAccessKey(reqEmpty))
}

// The admin UI mints access keys as "<random>/<random>". With the credential
// parsed from the left the scope components swallowed the key, the key was
// truncated to its first segment, and every V4 client was told
// InvalidAccessKeyId even though the rule was configured and correct.
func TestExtractAccessKey_KeyWithSlash(t *testing.T) {
	reqV4, _ := http.NewRequest("GET", "http://localhost:5246/logto/file.txt", nil)
	reqV4.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=5wyzj14nkv/EUEqnc8Nh/20261028/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=xxxx")
	assert.Equal(t, "5wyzj14nkv/EUEqnc8Nh", extractAccessKey(reqV4))

	reqV4Query, _ := http.NewRequest("GET", "http://localhost:5246/logto/file.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=5wyzj14nkv%2FEUEqnc8Nh%2F20261028%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Signature=xxxx", nil)
	assert.Equal(t, "5wyzj14nkv/EUEqnc8Nh", extractAccessKey(reqV4Query))

	// A key with several slashes keeps all of them: only the scope is stripped.
	reqMulti, _ := http.NewRequest("GET", "http://localhost:5246/logto/file.txt", nil)
	reqMulti.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=team/a/b/20261028/auto/s3/aws4_request, SignedHeaders=host, Signature=xxxx")
	assert.Equal(t, "team/a/b", extractAccessKey(reqMulti))
}

func TestV4CredentialAccessKey_Malformed(t *testing.T) {
	assert.Equal(t, "", v4CredentialAccessKey(""))
	assert.Equal(t, "", v4CredentialAccessKey("   "))
	// Fewer than five components: not a credential, keep the bare key as-is.
	assert.Equal(t, "barekey", v4CredentialAccessKey("barekey"))
	// No recognisable scope: the whole string stays the key (never truncated to
	// a prefix that might belong to somebody else).
	assert.Equal(t, "a/b", v4CredentialAccessKey(" a/b "))
}

func TestGetS3BucketAuthRules_Fallback(t *testing.T) {
	// When s3_bucket_auth is empty, fallback to s3_access_key_id / s3_secret_access_key
	//
	// The legacy pair is global state shared by the whole package: leaving it
	// behind adds a "legacy-ak" rule to every later test that counts or indexes
	// the parsed rules, so which tests pass would depend on the run order.
	t.Cleanup(func() {
		setTestSetting(conf.S3BucketAuth, "[]")
		setTestSetting(conf.S3AccessKeyId, "")
		setTestSetting(conf.S3SecretAccessKey, "")
	})
	setTestSetting(conf.S3BucketAuth, "[]")
	setTestSetting(conf.S3AccessKeyId, "legacy-ak")
	setTestSetting(conf.S3SecretAccessKey, "legacy-sk")

	rules := getS3BucketAuthRules()
	assert.Len(t, rules, 1)
	assert.Equal(t, "legacy-ak", rules[0].AccessKey)
	assert.Equal(t, "legacy-sk", rules[0].SecretKey)
	assert.Equal(t, []string{"*"}, rules[0].Buckets)
	assert.False(t, rules[0].ReadOnly, "a plain legacy key is read-write")

	pairs := resolveAuthPairs()
	assert.Equal(t, "legacy-sk", pairs["legacy-ak"])
}

func TestBucketAuthHandler_Permissions(t *testing.T) {
	rulesJSON := `[
		{
			"access_key": "app1-key",
			"secret_key": "app1-secret",
			"buckets": ["app1-bucket", "shared-*"],
			"readonly": false
		},
		{
			"access_key": "readonly-key",
			"secret_key": "readonly-secret",
			"buckets": ["app1-bucket"],
			"readonly": true
		}
	]`
	setTestSetting(conf.S3BucketAuth, rulesJSON)
	defer setTestSetting(conf.S3BucketAuth, "[]")

	handlerCalled := false
	publicCalled := false
	var capturedRule *S3BucketAuthRule
	testBackendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		capturedRule, _ = r.Context().Value(s3AuthRuleContextKey).(*S3BucketAuthRule)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	publicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("public"))
	})

	chained := bucketAuthHandler(testBackendHandler, publicHandler)

	// 1. Authorized GET request
	handlerCalled = false
	req1, _ := http.NewRequest("GET", "/app1-bucket/file.png", nil)
	req1.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app1-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	rec1 := httptest.NewRecorder()
	chained.ServeHTTP(rec1, req1)
	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, rec1.Code)
	assert.NotNil(t, capturedRule)
	assert.Equal(t, "app1-key", capturedRule.AccessKey)

	// 2. Unauthorized bucket GET request (Cross-bucket access denied)
	handlerCalled = false
	req2, _ := http.NewRequest("GET", "/private-bucket/secret.key", nil)
	req2.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app1-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	rec2 := httptest.NewRecorder()
	chained.ServeHTTP(rec2, req2)
	assert.False(t, handlerCalled)
	assert.Equal(t, http.StatusForbidden, rec2.Code)
	assert.Contains(t, rec2.Body.String(), "<Code>AccessDenied</Code>")
	assert.Contains(t, rec2.Body.String(), "<BucketName>private-bucket</BucketName>")

	// 3. ReadOnly key attempting write (PUT)
	handlerCalled = false
	req3, _ := http.NewRequest("PUT", "/app1-bucket/upload.txt", strings.NewReader("data"))
	req3.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=readonly-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	rec3 := httptest.NewRecorder()
	chained.ServeHTTP(rec3, req3)
	assert.False(t, handlerCalled)
	assert.Equal(t, http.StatusForbidden, rec3.Code)
	assert.Contains(t, rec3.Body.String(), "<Code>AccessDenied</Code>")

	// 4. Wildcard prefix authorized GET request
	handlerCalled = false
	req4, _ := http.NewRequest("GET", "/shared-logs/app.log", nil)
	req4.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app1-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	rec4 := httptest.NewRecorder()
	chained.ServeHTTP(rec4, req4)
	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, rec4.Code)

	// 5. Root ListBuckets request: should pass through and inject rule into Context
	handlerCalled = false
	req5, _ := http.NewRequest("GET", "/", nil)
	req5.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app1-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	rec5 := httptest.NewRecorder()
	chained.ServeHTTP(rec5, req5)
	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, rec5.Code)
	assert.NotNil(t, capturedRule)
	assert.Equal(t, "app1-key", capturedRule.AccessKey)

	// 6. Bucket-level request (ListObjects on a foreign bucket) must be denied.
	// parseObjectPath rejects "/bucket" so this used to slip through as a root
	// request and let any key enumerate every bucket.
	handlerCalled = false
	req6, _ := http.NewRequest("GET", "/private-bucket?list-type=2", nil)
	req6.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app1-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	rec6 := httptest.NewRecorder()
	chained.ServeHTTP(rec6, req6)
	assert.False(t, handlerCalled)
	assert.Equal(t, http.StatusForbidden, rec6.Code)
	assert.Contains(t, rec6.Body.String(), "<BucketName>private-bucket</BucketName>")

	// 7. Bucket-level request on an allowed bucket still works.
	handlerCalled = false
	req7, _ := http.NewRequest("GET", "/app1-bucket?list-type=2", nil)
	req7.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app1-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	rec7 := httptest.NewRecorder()
	chained.ServeHTTP(rec7, req7)
	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusOK, rec7.Code)

	// 8. Unknown access key is rejected here, not handed to gofakes3 (which
	// accepts anything once it has no keys registered).
	handlerCalled = false
	req8, _ := http.NewRequest("GET", "/app1-bucket/file.png", nil)
	req8.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=stale-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	rec8 := httptest.NewRecorder()
	chained.ServeHTTP(rec8, req8)
	assert.False(t, handlerCalled)
	assert.Equal(t, http.StatusForbidden, rec8.Code)
	assert.Contains(t, rec8.Body.String(), "<Code>InvalidAccessKeyId</Code>")
	assert.False(t, publicCalled)
}

func TestS3BucketAuthRule_JSONKeyNames(t *testing.T) {
	bucketsJSON := `[{"name":"pub","path":"/storage/pub","public":true},{"name":"priv","path":"/storage/priv"}]`
	setTestSetting(conf.S3Buckets, bucketsJSON)
	defer setTestSetting(conf.S3Buckets, "[]")
	// Isolate the parsing behaviour: a leftover legacy global key would add a
	// second rule and hide a broken parse.
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")

	for _, raw := range []string{
		`[{"access_key_id":"ak-canonical","secret_access_key":"sk-canonical","buckets":["*"]}]`,
		`[{"access_key":"ak-legacy","secret_key":"sk-legacy","buckets":["*"]}]`,
	} {
		setTestSetting(conf.S3BucketAuth, raw)
		rules := getS3BucketAuthRules()
		assert.Len(t, rules, 1, raw)
		assert.NotEmpty(t, rules[0].AccessKey, "rule parsed from %s lost its access key", raw)
		assert.NotEmpty(t, rules[0].SecretKey, "rule parsed from %s lost its secret", raw)
		assert.Equal(t, "sk-"+strings.TrimPrefix(rules[0].AccessKey, "ak-"), rules[0].SecretKey)
		// The admin UI writes access_key_id/secret_access_key; losing them used
		// to leave WithV4Auth with zero keys, which disables verification.
		assert.Len(t, resolveAuthPairs(), 1)
	}
	defer setTestSetting(conf.S3BucketAuth, "[]")

	setTestSetting(conf.S3Buckets, bucketsJSON)
	pub, err := getBucketByName("pub")
	assert.NoError(t, err)
	assert.True(t, pub.Public)
	assert.True(t, isPublicBucket("pub"))
	assert.False(t, isPublicBucket("priv"))
	assert.False(t, isPublicBucket("does-not-exist"))
}

func TestGetS3BucketAuthRules_KeepsLegacyGlobalKey(t *testing.T) {
	setTestSetting(conf.S3BucketAuth, `[{"access_key_id":"app-key","secret_access_key":"app-secret","buckets":["priv"]}]`)
	setTestSetting(conf.S3AccessKeyId, "master-key")
	setTestSetting(conf.S3SecretAccessKey, "master-secret")
	defer func() {
		setTestSetting(conf.S3BucketAuth, "[]")
		setTestSetting(conf.S3AccessKeyId, "")
		setTestSetting(conf.S3SecretAccessKey, "")
	}()

	pairs := resolveAuthPairs()
	// Adding a per-bucket key must not silently disable the master key: every
	// client still signing with it would start failing with 403.
	assert.Equal(t, "app-secret", pairs["app-key"])
	assert.Equal(t, "master-secret", pairs["master-key"])

	req, _ := http.NewRequest("GET", "/some-bucket/obj", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=master-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	assert.True(t, s3BucketAuthorized(req, "some-bucket"))
	assert.True(t, s3BucketReadAllowed(req, "some-bucket"))
}

func TestGetS3BucketAuthRules_MergesDuplicateAccessKeys(t *testing.T) {
	// The admin UI used to write one rule per bucket even when every row
	// carried the master key. Every lookup takes the first rule for a key, so
	// in that shape the key reached the first bucket only and 403ed on the
	// rest. Repeated keys must therefore be merged instead of shadowing.
	setTestSetting(conf.S3BucketAuth, `[
		{"access_key_id":"shared","secret_access_key":"shared-secret","buckets":["one"]},
		{"access_key_id":"shared","secret_access_key":"shared-secret","buckets":["two"]}
	]`)
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	defer func() {
		setTestSetting(conf.S3BucketAuth, "[]")
	}()

	rules := getS3BucketAuthRules()
	assert.Len(t, rules, 1)
	assert.Equal(t, "shared", rules[0].AccessKey)
	assert.ElementsMatch(t, []string{"one", "two"}, rules[0].Buckets)

	req, _ := http.NewRequest("GET", "/two/obj", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=shared/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	assert.True(t, s3BucketAuthorized(req, "two"))
	assert.True(t, s3BucketAuthorized(req, "one"))
}

func TestGetS3BucketAuthRules_MasterKeyOverridesPerBucketRows(t *testing.T) {
	// A per-bucket row saved with the master key must not narrow it: the
	// global key keeps full access to every bucket, read and write.
	setTestSetting(conf.S3BucketAuth, `[
		{"access_key_id":"master-key","secret_access_key":"master-secret","buckets":["one"],"readonly":true},
		{"access_key_id":"app-key","secret_access_key":"app-secret","buckets":["priv"]}
	]`)
	setTestSetting(conf.S3AccessKeyId, "master-key")
	setTestSetting(conf.S3SecretAccessKey, "master-secret")
	defer func() {
		setTestSetting(conf.S3BucketAuth, "[]")
		setTestSetting(conf.S3AccessKeyId, "")
		setTestSetting(conf.S3SecretAccessKey, "")
	}()

	rules := getS3BucketAuthRules()
	assert.Equal(t, "master-key", rules[0].AccessKey)
	assert.Equal(t, []string{"*"}, rules[0].Buckets)
	assert.True(t, rules[0].ReadOnly, "the read-only row for the master key must stick")
	assert.Equal(t, "app-key", rules[1].AccessKey)
	assert.Equal(t, []string{"priv"}, rules[1].Buckets)

	req, _ := http.NewRequest("PUT", "/anything/obj", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=master-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	assert.True(t, s3BucketAuthorized(req, "anything"))
	assert.False(t, s3BucketCanWrite(req), "a read-only master key must not write")

	// The scoped rule for the other key is unaffected.
	other, _ := http.NewRequest("PUT", "/priv/obj", nil)
	other.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	assert.True(t, s3BucketCanWrite(other))
}

func TestGetS3BucketAuthRules_DropsKeylessRules(t *testing.T) {
	// A rule with no access key authenticates nothing, so it is dropped.
	setTestSetting(conf.S3BucketAuth, `[{"buckets":["priv"]},{"access_key_id":"","secret_access_key":""}]`)
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	defer setTestSetting(conf.S3BucketAuth, "[]")

	assert.Empty(t, getS3BucketAuthRules())
	assert.Empty(t, resolveAuthPairs())
}

func TestBucketAuthHandler_RefusesWhenConfigIsUnusable(t *testing.T) {
	// Credentials are configured, so the operator believes the buckets are
	// protected, but nothing usable came out of it: a rule without a secret, a
	// legacy key without one, and unparseable JSON. Serving any of these as
	// anonymous access would hand out every bucket because of a typo, so the
	// gateway refuses the request instead.
	cases := map[string]func(){
		"rule without secret": func() {
			setTestSetting(conf.S3BucketAuth, `[{"access_key_id":"app-key","buckets":["priv"]}]`)
		},
		"legacy key without secret": func() {
			setTestSetting(conf.S3BucketAuth, "[]")
			setTestSetting(conf.S3AccessKeyId, "master-key")
			setTestSetting(conf.S3SecretAccessKey, "")
		},
		"broken json": func() {
			setTestSetting(conf.S3BucketAuth, `[{"access_key_id":`)
		},
	}

	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			setTestSetting(conf.S3BucketAuth, "[]")
			setTestSetting(conf.S3AccessKeyId, "")
			setTestSetting(conf.S3SecretAccessKey, "")
			setup()
			defer func() {
				setTestSetting(conf.S3BucketAuth, "[]")
				setTestSetting(conf.S3AccessKeyId, "")
				setTestSetting(conf.S3SecretAccessKey, "")
			}()

			rules, misconfigured := loadS3BucketAuthRules()
			assert.Empty(t, rules)
			assert.True(t, misconfigured)

			called := false
			chained := bucketAuthHandler(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) }),
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }),
			)
			req, _ := http.NewRequest("GET", "/priv/secret.txt", nil)
			rec := httptest.NewRecorder()
			chained.ServeHTTP(rec, req)
			assert.False(t, called, "a misconfigured gateway must not fall through to the anonymous path")
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

			// The helpers used by the redirect and CORS paths must agree.
			assert.False(t, s3BucketAuthorized(req, "priv"))
			assert.False(t, s3BucketCanWrite(req))
		})
	}
}

func TestRequestAccessKey_ResolvesOneIdentity(t *testing.T) {
	// Signed as one key, presigned as another: the request has no single
	// identity, so it is refused instead of being verified as one key and
	// authorised as the other.
	mixed, _ := http.NewRequest("GET", "/priv/obj", nil)
	mixed.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	q := mixed.URL.Query()
	q.Set("X-Amz-Credential", "victim-key/20261028/us-east-1/s3/aws4_request")
	q.Set("X-Amz-Signature", "yyy")
	mixed.URL.RawQuery = q.Encode()
	key, ok := requestAccessKey(mixed)
	assert.False(t, ok, "a request naming two keys must not be authorised at all")
	assert.Empty(t, key)
	assert.True(t, hasConflictingCredentials(mixed))

	headerOnly, _ := http.NewRequest("GET", "/priv/obj", nil)
	headerOnly.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app-key/20261028/us-east-1/s3/aws4_request, Signature=xxx")
	key, ok = requestAccessKey(headerOnly)
	assert.True(t, ok)
	assert.Equal(t, "app-key", key)

	queryOnly, _ := http.NewRequest("GET", "/priv/obj?X-Amz-Credential="+url.QueryEscape("victim-key/20261028/us-east-1/s3/aws4_request"), nil)
	key, ok = requestAccessKey(queryOnly)
	assert.True(t, ok)
	assert.Equal(t, "victim-key", key)

	// Both query spellings, naming different keys: gofakes3's V2 parser reads
	// AWSAccessKeyId while this one used to read X-Amz-Credential first, so this
	// shape was verified as one key and authorised as the other.
	bothQuery, _ := http.NewRequest("GET", "/priv/obj?X-Amz-Credential="+url.QueryEscape("master-key/20261028/us-east-1/s3/aws4_request")+"&AWSAccessKeyId=app-key", nil)
	_, ok = requestAccessKey(bothQuery)
	assert.False(t, ok, "two different query credentials must be refused")

	// The V2 header form without the space: gofakes3 strips spaces before it
	// looks for the AWS prefix, so this parses as app-key on both sides.
	unspaced, _ := http.NewRequest("GET", "/priv/obj", nil)
	unspaced.Header.Set("Authorization", "AWSapp-key:c2lnbmF0dXJl")
	key, ok = requestAccessKey(unspaced)
	assert.True(t, ok)
	assert.Equal(t, "app-key", key)

	anonymous, _ := http.NewRequest("GET", "/priv/obj", nil)
	_, ok = requestAccessKey(anonymous)
	assert.True(t, ok)
	assert.False(t, hasConflictingCredentials(anonymous))
}

func TestBucketAuthHandler_RejectsConflictingCredentials(t *testing.T) {
	setTestSetting(conf.S3BucketAuth, `[{"access_key_id":"app-key","secret_access_key":"app-secret","buckets":["priv"]}]`)
	defer setTestSetting(conf.S3BucketAuth, "[]")

	called := false
	chained := bucketAuthHandler(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }),
	)
	req, _ := http.NewRequest("GET", "/priv/obj", nil)
	req.Header.Set("Authorization", "AWS app-key:signature")
	q := req.URL.Query()
	q.Set("AWSAccessKeyId", "app-key")
	q.Set("Signature", "sig")
	q.Set("Expires", "1700000000")
	req.URL.RawQuery = q.Encode()

	rec := httptest.NewRecorder()
	chained.ServeHTTP(rec, req)
	assert.False(t, called)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestBucketAuthHandler_CopySourceMustBeAllowed(t *testing.T) {
	// A key scoped to "app" must not be able to read "shared" by copying out
	// of it, and a source without "/" must not reach gofakes3, which indexes
	// the parsed parts unchecked.
	setTestSetting(conf.S3BucketAuth, `[{"access_key_id":"app-key","secret_access_key":"app-secret","buckets":["app"]}]`)
	defer setTestSetting(conf.S3BucketAuth, "[]")

	called := false
	chained := bucketAuthHandler(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) }),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
	)

	newCopy := func(source string) *http.Request {
		req, _ := http.NewRequest("PUT", "/app/dst.txt", nil)
		// SignedHeaders has to name the copy source: Signature V4 authenticates
		// only the headers it lists, and gofakes3 rejects a V4 Authorization that
		// carries no SignedHeaders at all, so a fixture without it describes a
		// request that cannot exist.
		req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=app-key/20261028/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-copy-source, Signature=xxx")
		if source != "" {
			req.Header.Set("X-Amz-Copy-Source", source)
		}
		return req
	}

	// Same bucket: allowed.
	rec := httptest.NewRecorder()
	chained.ServeHTTP(rec, newCopy("/app/src.txt"))
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Another bucket: denied, and nothing reaches the storage layer.
	called = false
	rec = httptest.NewRecorder()
	chained.ServeHTTP(rec, newCopy("/shared/secret.txt"))
	assert.False(t, called)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Malformed header: rejected instead of panicking deeper in.
	called = false
	rec = httptest.NewRecorder()
	chained.ServeHTTP(rec, newCopy("no-slash-here"))
	assert.False(t, called)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// A key with a dot segment cannot be smuggled through the source either.
	called = false
	rec = httptest.NewRecorder()
	chained.ServeHTTP(rec, newCopy("/app/../shared/secret.txt"))
	assert.False(t, called)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestStorableMetadata_DropsRequestHeaders(t *testing.T) {
	headers := map[string]string{
		"x-amz-meta-owner":     "team-a",
		"Content-Type":         "text/plain",
		"Content-Disposition":  `attachment; filename="a.txt"`,
		"X-Amz-Copy-Source":    "/other-bucket/secret.bin",
		"X-Amz-Security-Token": "session-token",
		"X-Amz-Date":           "20261028T180000Z",
		"X-Amz-Content-Sha256": "abc",
		"Content-Length":       "12345",
		"Last-Modified":        "Wed, 28 Oct 2026 18:00:00 GMT",
		"X-Amz-Checksum-Crc32": "AAAAAA==",
	}

	stored := storableMetadata(headers)
	assert.Equal(t, "team-a", stored["x-amz-meta-owner"])
	assert.Equal(t, "text/plain", stored["Content-Type"])
	assert.Equal(t, `attachment; filename="a.txt"`, stored["Content-Disposition"])
	assert.Equal(t, "AAAAAA==", stored["X-Amz-Checksum-Crc32"])
	for _, dropped := range []string{"X-Amz-Copy-Source", "X-Amz-Security-Token", "X-Amz-Date", "X-Amz-Content-Sha256", "Content-Length", "Last-Modified"} {
		assert.NotContains(t, stored, dropped)
	}
}

func TestBucketAuthHandler_PublicBuckets(t *testing.T) {
	bucketsJSON := `[{"name":"pub","path":"/storage/pub","public":true},{"name":"priv","path":"/storage/priv"}]`
	rulesJSON := `[{"access_key_id":"app-key","secret_access_key":"app-secret","buckets":["priv"]}]`
	setTestSetting(conf.S3Buckets, bucketsJSON)
	setTestSetting(conf.S3BucketAuth, rulesJSON)
	defer func() {
		setTestSetting(conf.S3Buckets, "[]")
		setTestSetting(conf.S3BucketAuth, "[]")
	}()

	privateCalled := false
	publicCalled := false
	privateHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateCalled = true
		w.WriteHeader(http.StatusOK)
	})
	publicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicCalled = true
		w.WriteHeader(http.StatusOK)
	})
	chained := bucketAuthHandler(privateHandler, publicHandler)

	serve := func(method, target string) *httptest.ResponseRecorder {
		privateCalled, publicCalled = false, false
		req, _ := http.NewRequest(method, target, nil)
		rec := httptest.NewRecorder()
		chained.ServeHTTP(rec, req)
		return rec
	}

	// Anonymous GET on a public bucket is served by the keyless instance.
	rec := serve("GET", "/pub/index.html")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, publicCalled)
	assert.False(t, privateCalled)

	// HEAD and object listings too.
	rec = serve("HEAD", "/pub/index.html")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, publicCalled)

	rec = serve("GET", "/pub?list-type=2")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, publicCalled)

	// Anonymous writes are never allowed, even on a public bucket.
	for _, method := range []string{"PUT", "POST", "DELETE"} {
		rec = serve(method, "/pub/index.html")
		assert.Equal(t, http.StatusForbidden, rec.Code, method)
		assert.False(t, publicCalled, method)
		assert.False(t, privateCalled, method)
	}

	// Anonymous reads of a private bucket stay denied.
	rec = serve("GET", "/priv/index.html")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "<Code>AccessDenied</Code>")
	assert.False(t, publicCalled)

	// Anonymous ListBuckets stays denied so the bucket inventory is not public.
	rec = serve("GET", "/")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, privateCalled)
}

func TestPublicBucketGuard(t *testing.T) {
	bucketsJSON := `[{"name":"pub","path":"/storage/pub","public":true}]`
	setTestSetting(conf.S3Buckets, bucketsJSON)
	defer setTestSetting(conf.S3Buckets, "[]")

	reached := false
	guarded := publicBucketGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	check := func(method, target string, wantStatus int, wantReached bool) {
		reached = false
		req, _ := http.NewRequest(method, target, nil)
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, req)
		assert.Equal(t, wantStatus, rec.Code, "%s %s", method, target)
		assert.Equal(t, wantReached, reached, "%s %s", method, target)
	}

	check("GET", "/pub/a.txt", http.StatusOK, true)
	check("HEAD", "/pub/a.txt", http.StatusOK, true)
	check("PUT", "/pub/a.txt", http.StatusForbidden, false)
	check("DELETE", "/pub/a.txt", http.StatusForbidden, false)
	check("GET", "/other/a.txt", http.StatusForbidden, false)
	check("GET", "/", http.StatusForbidden, false)
}

func TestListBuckets_ViewFiltering(t *testing.T) {
	// Mock buckets
	testBuckets := []Bucket{
		{Name: "app1-bucket", Path: "/storage/app1"},
		{Name: "app2-bucket", Path: "/storage/app2"},
		{Name: "shared-data", Path: "/storage/shared"},
	}
	bucketsBytes, _ := json.Marshal(testBuckets)
	setTestSetting(conf.S3Buckets, string(bucketsBytes))

	backend := &s3Backend{}

	// Case 1: Filtered context for app1 (only allowed app1-bucket and shared-*)
	ruleApp1 := &S3BucketAuthRule{
		AccessKey: "app1",
		Buckets:   []string{"app1-bucket", "shared-*"},
	}
	ctx1 := context.WithValue(context.Background(), s3AuthRuleContextKey, ruleApp1)
	res1, err := backend.ListBuckets(ctx1)
	assert.NoError(t, err)
	assert.Len(t, res1, 2)
	assert.Equal(t, "app1-bucket", res1[0].Name)
	assert.Equal(t, "shared-data", res1[1].Name)

	// Case 2: Unfiltered context (e.g. no auth or wildcard)
	ruleAdmin := &S3BucketAuthRule{
		AccessKey: "admin",
		Buckets:   []string{"*"},
	}
	ctxAdmin := context.WithValue(context.Background(), s3AuthRuleContextKey, ruleAdmin)
	resAdmin, err := backend.ListBuckets(ctxAdmin)
	assert.NoError(t, err)
	assert.Len(t, resAdmin, 3)
}

func TestMergedRuleWithWildcardDropsPerBucketWritability(t *testing.T) {
	// Three rows for one key: a writable pair, a read-only bucket, then a
	// read-only wildcard. The wildcard decides the whole key, and it must not
	// leave behind the per-bucket writability computed from the earlier rows.
	for name, rows := range map[string]string{
		"wildcard last": `[
			{"access_key_id":"auditkeywild","secret_access_key":"s","buckets":["a","b"],"readonly":false},
			{"access_key_id":"auditkeywild","secret_access_key":"s","buckets":["a"],"readonly":true},
			{"access_key_id":"auditkeywild","secret_access_key":"s","buckets":["*"],"readonly":true}]`,
		"wildcard first": `[
			{"access_key_id":"auditkeywild","secret_access_key":"s","buckets":["*"],"readonly":true},
			{"access_key_id":"auditkeywild","secret_access_key":"s","buckets":["a","b"],"readonly":false},
			{"access_key_id":"auditkeywild","secret_access_key":"s","buckets":["a"],"readonly":true}]`,
	} {
		t.Run(name, func(t *testing.T) {
			setTestSetting(conf.S3BucketAuth, rows)
			defer setTestSetting(conf.S3BucketAuth, "[]")

			rules, misconfigured := loadS3BucketAuthRules()
			require.False(t, misconfigured)
			require.Len(t, rules, 1)
			assert.True(t, rules[0].ReadOnly, "a read-only wildcard row makes the whole key read-only")
			assert.Nil(t, rules[0].writableBuckets, "no stale per-bucket writability may survive the wildcard row")
			assert.False(t, rules[0].CanWriteBucket("b"), "row order must not change who can write")
			assert.False(t, rules[0].CanWriteBucket("a"))
		})
	}
}
