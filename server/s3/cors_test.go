package s3

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/gofakes3/signature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const corsTestBucket = "bucket"

// withCORSTestFixture registers one bucket and redirects the configuration
// write into the in-memory setting cache, so the tests never touch the DB.
func withCORSTestFixture(t *testing.T) {
	t.Helper()
	setTestSetting(conf.S3Buckets, `[{"name":"`+corsTestBucket+`","path":"/bucket"}]`)
	setTestSetting(conf.S3BucketAuth, "[]")
	setTestSetting(conf.S3BucketCors, "")
	// getS3BucketAuthRules falls back to the legacy single key, and
	// bucket_auth_test leaves it set, so a CORS test would otherwise inherit
	// credentials it never asked for.
	previousAccessKey := setting.GetStr(conf.S3AccessKeyId)
	previousSecretKey := setting.GetStr(conf.S3SecretAccessKey)
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")

	previous := saveS3CORSSetting
	saveS3CORSSetting = func(_ string, config map[string][]s3CORSRule) error {
		payload, err := json.Marshal(config)
		if err != nil {
			return err
		}
		setTestSetting(conf.S3BucketCors, string(payload))
		return nil
	}
	t.Cleanup(func() {
		saveS3CORSSetting = previous
		setTestSetting(conf.S3Buckets, "[]")
		setTestSetting(conf.S3BucketAuth, "[]")
		setTestSetting(conf.S3BucketCors, "")
		setTestSetting(conf.S3AccessKeyId, previousAccessKey)
		setTestSetting(conf.S3SecretAccessKey, previousSecretKey)
	})
}

func newCORSTestChain(inner http.Handler) http.Handler {
	if inner == nil {
		inner = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}
	return corsHandler(bucketAuthHandler(inner, inner))
}

func doCORSRequest(t *testing.T, handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func corsPreflight(t *testing.T, target, origin, method, headers string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodOptions, target, nil)
	req.Header.Set("Origin", origin)
	if method != "" {
		req.Header.Set("Access-Control-Request-Method", method)
	}
	if headers != "" {
		req.Header.Set("Access-Control-Request-Headers", headers)
	}
	return req
}

func TestCorsWildcardMatch(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{pattern: "*", value: "https://app.example", want: true},
		{pattern: "https://app.example", value: "https://app.example", want: true},
		{pattern: "https://app.example", value: "https://APP.example", want: true},
		{pattern: "https://app.example", value: "https://evil.example", want: false},
		{pattern: "https://*.example.com", value: "https://cdn.example.com", want: true},
		{pattern: "https://*.example.com", value: "https://example.com", want: false},
		{pattern: "https://*.example.com", value: "https://evil.com", want: false},
		{pattern: "x-amz-*", value: "x-amz-meta-foo", want: true},
		{pattern: "x-amz-*", value: "content-type", want: false},
		{pattern: "", value: "anything", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"|"+tt.value, func(t *testing.T) {
			assert.Equal(t, tt.want, corsWildcardMatch(tt.pattern, tt.value))
		})
	}
}

func TestDecideS3CORSDefaultRulesAllowBrowserRequest(t *testing.T) {
	decision := decideS3CORS(
		defaultS3CORSRules(),
		"https://app.example",
		http.MethodPut,
		[]string{"content-type", "x-amz-date", "x-amz-content-sha256", "range", "x-amz-meta-foo"},
	)
	require.True(t, decision.allowed())
	assert.Equal(t, "*", decision.origin)
	assert.Contains(t, decision.allowMethods, "PUT")
	assert.Contains(t, decision.allowHeaders, "range")
	assert.Contains(t, decision.allowHeaders, "x-amz-meta-foo")
	// ETag alone is not enough: ranged reads need Content-Range exposed.
	assert.Contains(t, decision.exposeHeaders, "ETag")
	assert.Contains(t, decision.exposeHeaders, "Accept-Ranges")
	assert.Contains(t, decision.exposeHeaders, "Content-Range")
	assert.Equal(t, s3CORSDefaultMaxAge, decision.maxAge)
}

func TestDecideS3CORSRejectsUnknownOriginAndMethod(t *testing.T) {
	rules := []s3CORSRule{{
		AllowedOrigins: []string{"https://app.example"},
		AllowedMethods: []string{"GET", "HEAD"},
		AllowedHeaders: []string{"*"},
	}}

	assert.False(t, decideS3CORS(rules, "https://evil.example", http.MethodGet, nil).allowed())
	assert.False(t, decideS3CORS(rules, "https://app.example", http.MethodPut, nil).allowed())

	echoed := decideS3CORS(rules, "https://app.example", http.MethodGet, nil)
	require.True(t, echoed.allowed())
	// A concrete origin must be echoed, never widened to "*": the wildcard is
	// what made the old behaviour impossible to restrict.
	assert.Equal(t, "https://app.example", echoed.origin)
}

// TestCorsDefaultRuleInheritsGlobalCorsConfig keeps deployments that narrowed
// the instance-wide `cors` config working: before this patch that gin middleware
// was the only CORS gate, so the inherited default must reproduce it.
func TestCorsDefaultRuleInheritsGlobalCorsConfig(t *testing.T) {
	previous := conf.Conf.Cors
	t.Cleanup(func() { conf.Conf.Cors = previous })

	conf.Conf.Cors.AllowOrigins = []string{"https://only.example"}
	conf.Conf.Cors.AllowMethods = []string{"GET", "HEAD"}
	conf.Conf.Cors.AllowHeaders = []string{"content-type", "x-amz-date"}

	inherited := defaultS3CORSRules()[0]
	assert.Equal(t, []string{"https://only.example"}, inherited.AllowedOrigins)
	assert.Equal(t, []string{"GET", "HEAD"}, inherited.AllowedMethods)
	assert.Equal(t, []string{"content-type", "x-amz-date"}, inherited.AllowedHeaders)

	allowed := decideS3CORS(defaultS3CORSRules(), "https://only.example", http.MethodGet, nil)
	require.True(t, allowed.allowed())
	assert.Equal(t, "https://only.example", allowed.origin)
	assert.False(t, decideS3CORS(defaultS3CORSRules(), "https://other.example", http.MethodGet, nil).allowed())
	assert.False(t, decideS3CORS(defaultS3CORSRules(), "https://only.example", http.MethodPut, nil).allowed())

	// gin's own default is allow_methods ["*"]; a bare "*" has to widen to the
	// S3 verb list instead of matching no method at all.
	conf.Conf.Cors.AllowMethods = []string{"*"}
	assert.Equal(t, s3CORSDefaultMethods, defaultS3CORSRules()[0].AllowedMethods)
	assert.True(t, decideS3CORS(defaultS3CORSRules(), "https://only.example", http.MethodPut, nil).allowed())
}

// TestCorsPreflightIsAnsweredBeforeAuthentication is the core regression: with
// access keys configured gofakes3 would reject the unsigned preflight with 403,
// which breaks every browser client.
func TestCorsPreflightIsAnsweredBeforeAuthentication(t *testing.T) {
	withCORSTestFixture(t)
	setTestSetting(conf.S3BucketAuth,
		`[{"access_key_id":"app-key","secret_access_key":"secret","buckets":["bucket"],"readonly":false}]`)

	reached := false
	chain := newCORSTestChain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	recorder := doCORSRequest(t, chain, corsPreflight(t, "/bucket/file.txt", "https://app.example", "PUT", "content-type, x-amz-date"))
	require.False(t, reached, "preflight must not reach the signed request path")
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "*", recorder.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, recorder.Header().Get("Access-Control-Allow-Methods"), "PUT")
	assert.Contains(t, recorder.Header().Get("Access-Control-Allow-Headers"), "x-amz-date")
	assert.NotEmpty(t, recorder.Header().Get("Access-Control-Max-Age"))
	assert.Contains(t, recorder.Header().Get("Vary"), "Access-Control-Request-Method")

	// The actual request is still authenticated: anonymous PUT is denied, but
	// the denial now carries CORS headers so the browser can read the error.
	put := httptest.NewRequest(http.MethodPut, "/bucket/file.txt", strings.NewReader("x"))
	put.Header.Set("Origin", "https://app.example")
	denied := doCORSRequest(t, chain, put)
	require.Equal(t, http.StatusForbidden, denied.Code)
	assert.Equal(t, "*", denied.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, denied.Body.String(), "AccessDenied")
}

func TestCorsExplicitOriginIsEchoedWithVary(t *testing.T) {
	withCORSTestFixture(t)
	body := `<CORSConfiguration><CORSRule>` +
		`<AllowedOrigin>https://app.example</AllowedOrigin>` +
		`<AllowedMethod>GET</AllowedMethod>` +
		`<AllowedMethod>HEAD</AllowedMethod>` +
		`<AllowedHeader>authorization</AllowedHeader>` +
		`<ExposeHeader>ETag</ExposeHeader>` +
		`<MaxAgeSeconds>600</MaxAgeSeconds>` +
		`</CORSRule></CORSConfiguration>`
	put := httptest.NewRequest(http.MethodPut, "/bucket?cors", strings.NewReader(body))
	require.Equal(t, http.StatusOK, doCORSRequest(t, corsHandler(http.NotFoundHandler()), put).Code)

	allowed := doCORSRequest(t, corsHandler(http.NotFoundHandler()),
		corsPreflight(t, "/bucket/file.txt", "https://app.example", "GET", "authorization"))
	require.Equal(t, http.StatusOK, allowed.Code)
	assert.Equal(t, "https://app.example", allowed.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "600", allowed.Header().Get("Access-Control-Max-Age"))
	assert.Contains(t, allowed.Header().Get("Vary"), "Origin")

	otherOrigin := doCORSRequest(t, corsHandler(http.NotFoundHandler()),
		corsPreflight(t, "/bucket/file.txt", "https://evil.example", "GET", ""))
	assert.Equal(t, http.StatusForbidden, otherOrigin.Code)
	assert.Empty(t, otherOrigin.Header().Get("Access-Control-Allow-Origin"))

	otherMethod := doCORSRequest(t, corsHandler(http.NotFoundHandler()),
		corsPreflight(t, "/bucket/file.txt", "https://app.example", "DELETE", ""))
	assert.Equal(t, http.StatusForbidden, otherMethod.Code)
	assert.Empty(t, otherMethod.Header().Get("Access-Control-Allow-Origin"))
}

func TestCorsConfigurationRoundTrip(t *testing.T) {
	withCORSTestFixture(t)
	chain := corsHandler(http.NotFoundHandler())

	missing := doCORSRequest(t, chain, httptest.NewRequest(http.MethodGet, "/bucket?cors", nil))
	require.Equal(t, http.StatusNotFound, missing.Code)
	assert.Contains(t, missing.Body.String(), "NoSuchCORSConfiguration")

	body := `<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<CORSRule><ID>web</ID>` +
		`<AllowedOrigin>https://app.example</AllowedOrigin>` +
		`<AllowedMethod>GET</AllowedMethod>` +
		`<AllowedMethod>put</AllowedMethod>` +
		`<AllowedHeader>*</AllowedHeader>` +
		`</CORSRule></CORSConfiguration>`
	put := doCORSRequest(t, chain, httptest.NewRequest(http.MethodPut, "/bucket?cors", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, put.Code)

	stored := getS3CORSConfiguration()[corsTestBucket]
	require.Len(t, stored, 1)
	// Methods are normalised to upper case on the way in.
	assert.Equal(t, []string{"GET", "PUT"}, stored[0].AllowedMethods)

	get := doCORSRequest(t, chain, httptest.NewRequest(http.MethodGet, "/bucket?cors", nil))
	require.Equal(t, http.StatusOK, get.Code)
	var config s3CORSConfiguration
	require.NoError(t, xml.Unmarshal(get.Body.Bytes(), &config))
	require.Len(t, config.Rules, 1)
	assert.Equal(t, "https://app.example", config.Rules[0].AllowedOrigins[0])
	assert.Equal(t, []string{"GET", "PUT"}, config.Rules[0].AllowedMethods)

	del := doCORSRequest(t, chain, httptest.NewRequest(http.MethodDelete, "/bucket?cors", nil))
	require.Equal(t, http.StatusNoContent, del.Code)
	assert.Empty(t, getS3CORSConfiguration())

	gone := doCORSRequest(t, chain, httptest.NewRequest(http.MethodGet, "/bucket?cors", nil))
	assert.Equal(t, http.StatusNotFound, gone.Code)
}

func TestCorsConfigurationValidation(t *testing.T) {
	withCORSTestFixture(t)
	chain := corsHandler(http.NotFoundHandler())

	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "no rule", body: "<CORSConfiguration></CORSConfiguration>"},
		{name: "no origin", body: "<CORSConfiguration><CORSRule><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>"},
		{name: "no method", body: "<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin></CORSRule></CORSConfiguration>"},
		{name: "bad method", body: "<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>PATCH</AllowedMethod></CORSRule></CORSConfiguration>"},
		{name: "negative max age", body: "<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod><MaxAgeSeconds>-1</MaxAgeSeconds></CORSRule></CORSConfiguration>"},
		{name: "not xml", body: "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := doCORSRequest(t, chain, httptest.NewRequest(http.MethodPut, "/bucket?cors", strings.NewReader(tt.body)))
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Contains(t, recorder.Body.String(), "MalformedXML")
		})
	}
}

func TestCorsConfigurationUnknownBucket(t *testing.T) {
	withCORSTestFixture(t)
	chain := corsHandler(http.NotFoundHandler())

	recorder := doCORSRequest(t, chain, httptest.NewRequest(http.MethodGet, "/nope?cors", nil))
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "NoSuchBucket")
}

func TestCorsConfigurationRequiresAuthorization(t *testing.T) {
	withCORSTestFixture(t)
	setTestSetting(conf.S3BucketAuth,
		`[{"access_key_id":"ro-key","secret_access_key":"ro-secret","buckets":["bucket"],"readonly":true},`+
			`{"access_key_id":"rw-key","secret_access_key":"rw-secret","buckets":["bucket"],"readonly":false}]`)
	chain := corsHandler(http.NotFoundHandler())

	anonymous := doCORSRequest(t, chain, httptest.NewRequest(http.MethodGet, "/bucket?cors", nil))
	assert.Equal(t, http.StatusForbidden, anonymous.Code)

	// The access key ID is public in every request: only the secret proves who
	// the caller is, so a wrong secret has to be refused rather than accepted
	// because the bucket happens to match.
	wrongSecret := httptest.NewRequest(http.MethodGet, "/bucket?cors", nil)
	signV2Request(t, wrongSecret, "ro-key", "not-the-secret")
	refused := doCORSRequest(t, chain, wrongSecret)
	assert.Equal(t, http.StatusForbidden, refused.Code)
	assert.Contains(t, refused.Body.String(), "SignatureDoesNotMatch")

	// A correctly signed read-only key may read, never write.
	readOnlyGet := httptest.NewRequest(http.MethodGet, "/bucket?cors", nil)
	signV2Request(t, readOnlyGet, "ro-key", "ro-secret")
	assert.Equal(t, http.StatusNotFound, doCORSRequest(t, chain, readOnlyGet).Code)

	readOnlyPut := httptest.NewRequest(http.MethodPut, "/bucket?cors",
		strings.NewReader(corsTestConfigurationBody))
	signV2Request(t, readOnlyPut, "ro-key", "ro-secret")
	assert.Equal(t, http.StatusForbidden, doCORSRequest(t, chain, readOnlyPut).Code)

	writeKey := httptest.NewRequest(http.MethodPut, "/bucket?cors",
		strings.NewReader(corsTestConfigurationBody))
	signV2Request(t, writeKey, "rw-key", "rw-secret")
	assert.Equal(t, http.StatusOK, doCORSRequest(t, chain, writeKey).Code)
}

// TestCorsConfigurationResponsesCarryCORSHeaders: the `?cors` subresource is
// answered before gofakes3, so without wrapping its writer a browser could not
// read either the configuration or the error that explains why it was refused.
func TestCorsConfigurationResponsesCarryCORSHeaders(t *testing.T) {
	withCORSTestFixture(t)
	setTestSetting(conf.S3BucketCors,
		`{"bucket":[{"allowed_origins":["https://app.example"],"allowed_methods":["GET"]}]}`)
	setTestSetting(conf.S3BucketAuth,
		`[{"access_key_id":"rw-key","secret_access_key":"rw-secret","buckets":["bucket"]}]`)
	chain := corsHandler(http.NotFoundHandler())

	configured := httptest.NewRequest(http.MethodGet, "/bucket?cors", nil)
	configured.Header.Set("Origin", "https://app.example")
	signV2Request(t, configured, "rw-key", "rw-secret")
	recorder := doCORSRequest(t, chain, configured)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "CORSConfiguration")
	assert.Equal(t, "https://app.example", recorder.Header().Get("Access-Control-Allow-Origin"))

	// An unsigned call is refused, but the refusal stays readable in a browser.
	unsigned := httptest.NewRequest(http.MethodPut, "/bucket?cors",
		strings.NewReader(corsTestConfigurationBody))
	unsigned.Header.Set("Origin", "https://app.example")
	refused := doCORSRequest(t, chain, unsigned)
	assert.Equal(t, http.StatusForbidden, refused.Code)
	assert.Equal(t, "https://app.example", refused.Header().Get("Access-Control-Allow-Origin"))
}

// TestCorsHeadersOverrideInnerHandler proves the middleware owns the headers:
// gofakes3 always writes `Access-Control-Allow-Origin: *`, which would keep a
// restricted bucket unrestricted if it survived.
func TestCorsHeadersOverrideInnerHandler(t *testing.T) {
	withCORSTestFixture(t)
	setTestSetting(conf.S3BucketCors, `{"*":[{"allowed_origins":["https://app.example"],"allowed_methods":["GET"]}]}`)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Expose-Headers", "ETag")
		w.WriteHeader(http.StatusOK)
	})

	allowed := httptest.NewRequest(http.MethodGet, "/bucket/file.txt", nil)
	allowed.Header.Set("Origin", "https://app.example")
	recorder := doCORSRequest(t, corsHandler(inner), allowed)
	assert.Equal(t, "https://app.example", recorder.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, recorder.Header().Get("Vary"), "Origin")
	// The bucket rule lists no ExposeHeader, so the inner value must not leak.
	assert.Empty(t, recorder.Header().Get("Access-Control-Expose-Headers"))

	denied := httptest.NewRequest(http.MethodGet, "/bucket/file.txt", nil)
	denied.Header.Set("Origin", "https://evil.example")
	recorder = doCORSRequest(t, corsHandler(inner), denied)
	assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))

	// Same-origin (or non-browser) requests keep gofakes3's headers untouched.
	plain := httptest.NewRequest(http.MethodGet, "/bucket/file.txt", nil)
	recorder = doCORSRequest(t, corsHandler(inner), plain)
	assert.Equal(t, "*", recorder.Header().Get("Access-Control-Allow-Origin"))
}

func TestCorsHeadersOnRedirect(t *testing.T) {
	withCORSTestFixture(t)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://cdn.example/file.txt")
		w.WriteHeader(http.StatusFound)
	})

	req := httptest.NewRequest(http.MethodGet, "/bucket/file.txt", nil)
	req.Header.Set("Origin", "https://app.example")
	recorder := doCORSRequest(t, corsHandler(inner), req)

	require.Equal(t, http.StatusFound, recorder.Code)
	assert.Equal(t, "https://cdn.example/file.txt", recorder.Header().Get("Location"))
	assert.Equal(t, "*", recorder.Header().Get("Access-Control-Allow-Origin"))
	assert.NotEmpty(t, recorder.Header().Get("Access-Control-Expose-Headers"))
}

func TestCorsRulesForBucketFallback(t *testing.T) {
	withCORSTestFixture(t)

	// Nothing configured: the built-in permissive rule keeps old deployments
	// working identically.
	assert.Equal(t, "*", corsRulesForBucket("anything")[0].AllowedOrigins[0])
	assert.False(t, hasS3CORSConfiguration("anything"))

	// The wildcard key applies to buckets without rules of their own.
	setTestSetting(conf.S3BucketCors, `{"*":[{"allowed_origins":["https://wild.example"],"allowed_methods":["GET"]}]}`)
	assert.Equal(t, []string{"https://wild.example"}, corsRulesForBucket("anything")[0].AllowedOrigins)
	assert.True(t, hasS3CORSConfiguration("anything"))

	// A bucket's own rules win over the wildcard.
	setTestSetting(conf.S3BucketCors,
		`{"*":[{"allowed_origins":["https://wild.example"],"allowed_methods":["GET"]}],`+
			`"bucket":[{"allowed_origins":["https://own.example"],"allowed_methods":["PUT"]}]}`)
	assert.Equal(t, []string{"https://own.example"}, corsRulesForBucket("bucket")[0].AllowedOrigins)
	assert.Equal(t, []string{"https://wild.example"}, corsRulesForBucket("other")[0].AllowedOrigins)

	// Malformed JSON must fall back to the default instead of panicking.
	setTestSetting(conf.S3BucketCors, "{not json")
	assert.Equal(t, "*", corsRulesForBucket("bucket")[0].AllowedOrigins[0])
}

func TestHasNonObjectQueryCoversCORS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/bucket/file.txt?cors", nil)
	assert.True(t, hasNonObjectQuery(req))
}

func TestCorsPreflightWithoutRequestedMethod(t *testing.T) {
	withCORSTestFixture(t)
	recorder := doCORSRequest(t, corsHandler(http.NotFoundHandler()),
		corsPreflight(t, "/bucket/file.txt", "https://app.example", "", ""))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "Access-Control-Request-Method")
}

func TestCorsPublicBucketPreflight(t *testing.T) {
	withCORSTestFixture(t)
	setTestSetting(conf.S3Buckets, `[{"name":"bucket","path":"/bucket","public":true}]`)
	setTestSetting(conf.S3BucketAuth,
		`[{"access_key_id":"app-key","secret_access_key":"secret","buckets":["bucket"],"readonly":false}]`)

	// Anonymous reads of a public bucket stay readable cross-origin, and the
	// preflight for them succeeds without credentials.
	preflight := doCORSRequest(t, newCORSTestChain(nil),
		corsPreflight(t, "/bucket/photo.jpg", "https://app.example", "GET", ""))
	assert.Equal(t, http.StatusOK, preflight.Code)
	assert.Equal(t, "*", preflight.Header().Get("Access-Control-Allow-Origin"))

	get := httptest.NewRequest(http.MethodGet, "/bucket/photo.jpg", nil)
	get.Header.Set("Origin", "https://app.example")
	recorder := doCORSRequest(t, newCORSTestChain(nil), get)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "*", recorder.Header().Get("Access-Control-Allow-Origin"))
}

const corsTestConfigurationBody = `<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin>` +
	`<AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`

// signV2Request signs req with AWS Signature Version 2. The tests only need a
// signature that the verifier accepts; the Signature V4 path is exercised
// end-to-end against a running instance.
func signV2Request(t *testing.T, req *http.Request, accessKey, secretKey string) {
	t.Helper()
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	cred := signature.CredentialsV2{AccessKey: accessKey, SecretKey: secretKey}
	sig := cred.SignV2(req.Method, req.URL.EscapedPath(), req.URL.RawQuery, req.Header, "")
	req.Header.Set("Authorization", "AWS "+accessKey+":"+sig)
}
