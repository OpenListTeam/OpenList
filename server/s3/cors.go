package s3

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/gofakes3/signature"
	log "github.com/sirupsen/logrus"
)

// CORS support for the S3 gateway.
//
// The gateway sits on top of gofakes3, which already answers every response
// with a hard-coded `Access-Control-Allow-Origin: *` and a fixed header
// whitelist. That is not enough for a browser client:
//
//   - when the gateway has access keys configured, gofakes3 verifies the V4
//     signature on the preflight OPTIONS request too. Browsers never sign a
//     preflight, so every real browser upload fails with 403.
//   - the whitelist has no `Range`, no `x-amz-copy-source` and no
//     `x-amz-meta-*`, so ranged reads and metadata writes break.
//   - `Expose-Headers` only carries `ETag`, so JS cannot read
//     `Content-Range` / `x-amz-*` response headers.
//   - the header is written by the inner handler, i.e. below the
//     redirectHandler that answers direct-upload and direct-download requests,
//     so 302/307 responses carry no CORS headers at all.
//
// This file replaces that behaviour with a per-bucket configuration that is
// honoured by an outermost middleware, so preflight is answered before any
// signature check, the redirects inherit the same headers, and
// `?cors` is a real S3 subresource (PUT / GET / DELETE).
//
// Because the previous behaviour was "every origin is allowed", the default is
// a wide-open rule for buckets that have no configuration of their own. A
// bucket only becomes restrictive once someone calls PutBucketCors with a
// narrower origin list.

// s3CORSWildcardBucket is the configuration key whose rules apply to every
// bucket that has no rules of its own.
const s3CORSWildcardBucket = "*"

const (
	s3CORSBodyLimit     = 1 << 20 // 1 MiB, same ceiling as a config document
	s3CORSMaxRules      = 100     // S3's own limit
	s3CORSDefaultMaxAge = 3000
)

// s3CORSRule is one <CORSRule> entry of a bucket CORS configuration.
type s3CORSRule struct {
	ID             string   `xml:"ID,omitempty" json:"id,omitempty"`
	AllowedOrigins []string `xml:"AllowedOrigin" json:"allowed_origins"`
	AllowedMethods []string `xml:"AllowedMethod" json:"allowed_methods"`
	AllowedHeaders []string `xml:"AllowedHeader,omitempty" json:"allowed_headers,omitempty"`
	ExposeHeaders  []string `xml:"ExposeHeader,omitempty" json:"expose_headers,omitempty"`
	MaxAgeSeconds  int      `xml:"MaxAgeSeconds,omitempty" json:"max_age_seconds,omitempty"`
}

// s3CORSConfiguration is the document exchanged by PutBucketCors and
// GetBucketCors.
type s3CORSConfiguration struct {
	XMLName xml.Name     `xml:"CORSConfiguration"`
	XMLNS   string       `xml:"xmlns,attr,omitempty"`
	Rules   []s3CORSRule `xml:"CORSRule"`
}

const s3CORSXMLNS = "http://s3.amazonaws.com/doc/2006-03-01/"

// s3CORSDefaultExposeHeaders are the response headers a browser client needs
// but which are not CORS-safelisted, so they must be listed explicitly.
var s3CORSDefaultExposeHeaders = []string{
	"ETag",
	"Accept-Ranges",
	"Content-Range",
	"Content-Disposition",
	"Last-Modified",
	"x-amz-request-id",
	"x-amz-id-2",
	"x-amz-version-id",
}

// s3CORSDefaultMethods are the S3 verbs a rule may list. `OPTIONS` is never
// part of a CORS rule: the preflight is answered from the rule that covers the
// real method.
var s3CORSDefaultMethods = []string{"GET", "HEAD", "PUT", "POST", "DELETE"}

// defaultS3CORSRules reproduces what the gateway allowed before per-bucket
// rules existed. Before this patch the gate was gin-contrib/cors driven by the
// instance-wide `cors` config, and gofakes3 added a fixed, wide-open header
// set on top, so the inherited default is that same global configuration
// (which is `*` unless the operator narrowed it) with the S3 verb list.
func defaultS3CORSRules() []s3CORSRule {
	origins := append([]string(nil), conf.Conf.Cors.AllowOrigins...)
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	methods := s3CORSDefaultMethods
	if configured := conf.Conf.Cors.AllowMethods; len(configured) > 0 && !containsWildcard(configured) {
		methods = append([]string(nil), configured...)
	}
	headers := append([]string(nil), conf.Conf.Cors.AllowHeaders...)
	if len(headers) == 0 {
		headers = []string{"*"}
	}
	return []s3CORSRule{{
		AllowedOrigins: origins,
		AllowedMethods: append([]string(nil), methods...),
		AllowedHeaders: headers,
		ExposeHeaders:  append([]string(nil), s3CORSDefaultExposeHeaders...),
		MaxAgeSeconds:  s3CORSDefaultMaxAge,
	}}
}

// containsWildcard reports whether list contains a bare "*" entry.
func containsWildcard(list []string) bool {
	for _, item := range list {
		if strings.TrimSpace(item) == "*" {
			return true
		}
	}
	return false
}

// corsSettingMu serialises the read-modify-write of the JSON setting so two
// concurrent PutBucketCors calls on different buckets cannot drop each other.
var corsSettingMu sync.Mutex

// saveS3CORSSetting persists the per-bucket configuration. It is a variable so
// tests can stub the database write.
var saveS3CORSSetting = func(config map[string][]s3CORSRule) error {
	payload, err := json.Marshal(config)
	if err != nil {
		return err
	}
	item, err := op.GetSettingItemByKey(conf.S3BucketCors)
	if err != nil || item == nil {
		item = &model.SettingItem{
			Key:   conf.S3BucketCors,
			Type:  conf.TypeText,
			Group: model.S3,
			Flag:  model.PRIVATE,
		}
	} else {
		// do not mutate the cached item in place: it is shared with readers
		copied := *item
		item = &copied
	}
	item.Value = string(payload)
	return op.SaveSettingItem(item)
}

// getS3CORSConfiguration returns the parsed setting, keyed by bucket name.
func getS3CORSConfiguration() map[string][]s3CORSRule {
	config := map[string][]s3CORSRule{}
	raw := strings.TrimSpace(setting.GetStr(conf.S3BucketCors))
	if raw == "" {
		return config
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		log.Warnf("failed to parse %s: %v", conf.S3BucketCors, err)
		return map[string][]s3CORSRule{}
	}
	return config
}

// corsRulesForBucket resolves the rules that apply to a bucket: its own rules
// when it has any, otherwise the wildcard rules, otherwise the built-in
// permissive default.
func corsRulesForBucket(bucket string) []s3CORSRule {
	config := getS3CORSConfiguration()
	if rules, ok := config[bucket]; ok && len(rules) > 0 {
		return rules
	}
	if rules, ok := config[s3CORSWildcardBucket]; ok && len(rules) > 0 {
		return rules
	}
	return defaultS3CORSRules()
}

// hasS3CORSConfiguration reports whether the bucket (or the wildcard key) has
// an explicit configuration, which is what GetBucketCors must expose.
func hasS3CORSConfiguration(bucket string) bool {
	config := getS3CORSConfiguration()
	if rules, ok := config[bucket]; ok && len(rules) > 0 {
		return true
	}
	return config[s3CORSWildcardBucket] != nil && len(config[s3CORSWildcardBucket]) > 0
}

// corsWildcardMatch reports whether value matches pattern, where pattern may
// contain a single '*'. Case is folded because header names are
// case-insensitive and origin hosts are compared case-insensitively by
// browsers.
func corsWildcardMatch(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	idx := strings.IndexByte(pattern, '*')
	if idx < 0 {
		return strings.EqualFold(pattern, value)
	}
	prefix, suffix := pattern[:idx], pattern[idx+1:]
	if len(value) < len(prefix)+len(suffix) {
		return false
	}
	if !strings.EqualFold(value[:len(prefix)], prefix) {
		return false
	}
	return strings.EqualFold(value[len(value)-len(suffix):], suffix)
}

// allowsOrigin reports whether the rule matches origin and which value the
// Access-Control-Allow-Origin header must carry. A rule listing "*" keeps the
// wildcard (cacheable, no Vary needed); otherwise the request origin is echoed.
func (r *s3CORSRule) allowsOrigin(origin string) (string, bool) {
	matched := ""
	for _, allowed := range r.AllowedOrigins {
		if strings.TrimSpace(allowed) == "*" {
			return "*", true
		}
		if corsWildcardMatch(allowed, origin) {
			matched = origin
		}
	}
	if matched == "" {
		return "", false
	}
	return matched, true
}

func (r *s3CORSRule) allowsMethod(method string) bool {
	for _, allowed := range r.AllowedMethods {
		if strings.EqualFold(strings.TrimSpace(allowed), method) {
			return true
		}
	}
	return false
}

func (r *s3CORSRule) allowsHeaders(headers []string) bool {
	for _, header := range headers {
		allowed := false
		for _, pattern := range r.AllowedHeaders {
			if corsWildcardMatch(pattern, header) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

// s3CORSDecision is the set of headers to emit for one request. An empty
// origin means "no rule matches", i.e. the response must carry no CORS headers
// at all so the browser rejects it.
type s3CORSDecision struct {
	origin        string
	allowMethods  string
	allowHeaders  string
	exposeHeaders string
	maxAge        int
}

func (d s3CORSDecision) allowed() bool { return d.origin != "" }

// decideS3CORS picks the rules that match the request and merges their
// directives the way S3 does: origins are the union, methods and exposed
// headers are the union of every matching rule, MaxAgeSeconds is the largest.
func decideS3CORS(rules []s3CORSRule, origin, method string, requestedHeaders []string) s3CORSDecision {
	if origin == "" {
		return s3CORSDecision{}
	}
	var (
		matched []*s3CORSRule
		acao    string
	)
	for i := range rules {
		rule := &rules[i]
		value, ok := rule.allowsOrigin(origin)
		if !ok {
			continue
		}
		if method != "" && !rule.allowsMethod(method) {
			continue
		}
		matched = append(matched, rule)
		if value == "*" {
			acao = "*"
		} else if acao == "" {
			acao = value
		}
	}
	if len(matched) == 0 || acao == "" {
		return s3CORSDecision{}
	}

	decision := s3CORSDecision{origin: acao}
	var methods, expose []string
	for _, rule := range matched {
		methods = appendUniqueFold(methods, rule.AllowedMethods...)
		expose = appendUniqueFold(expose, rule.ExposeHeaders...)
		if rule.MaxAgeSeconds > decision.maxAge {
			decision.maxAge = rule.MaxAgeSeconds
		}
	}
	decision.allowMethods = strings.Join(methods, ", ")
	decision.exposeHeaders = strings.Join(expose, ", ")

	if len(requestedHeaders) > 0 {
		for _, rule := range matched {
			if rule.allowsHeaders(requestedHeaders) {
				decision.allowHeaders = strings.Join(requestedHeaders, ", ")
				break
			}
		}
	} else {
		decision.allowHeaders = strings.Join(appendUniqueFold(nil, matched[0].AllowedHeaders...), ", ")
	}
	return decision
}

// appendUniqueFold appends values to dst, skipping blanks and values that are
// already present (case-insensitively), preserving the original order.
func appendUniqueFold(dst []string, values ...string) []string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		duplicate := false
		for _, existing := range dst {
			if strings.EqualFold(existing, value) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dst = append(dst, value)
		}
	}
	return dst
}

// corsHeaderNames are the response headers this middleware owns; anything the
// inner handlers wrote must be stripped before the final decision is applied.
var corsHeaderNames = []string{
	"Access-Control-Allow-Origin",
	"Access-Control-Allow-Methods",
	"Access-Control-Allow-Headers",
	"Access-Control-Allow-Credentials",
	"Access-Control-Expose-Headers",
	"Access-Control-Max-Age",
}

// corsResponseWriter applies the decision when the response header is written,
// which is late enough to override gofakes3's own CORS headers but early
// enough to cover error responses from the auth middleware and the 302/307
// redirects produced by redirectHandler.
type corsResponseWriter struct {
	http.ResponseWriter
	decision    s3CORSDecision
	wroteHeader bool
}

func (w *corsResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.applyCORSHeaders()
	w.ResponseWriter.WriteHeader(status)
}

func (w *corsResponseWriter) applyCORSHeaders() {
	header := w.Header()
	for _, name := range corsHeaderNames {
		header.Del(name)
	}
	decision := w.decision
	if !decision.allowed() {
		return
	}
	header.Set("Access-Control-Allow-Origin", decision.origin)
	if decision.origin != "*" {
		header.Add("Vary", "Origin")
	}
	if decision.allowMethods != "" {
		header.Set("Access-Control-Allow-Methods", decision.allowMethods)
	}
	if decision.allowHeaders != "" {
		header.Set("Access-Control-Allow-Headers", decision.allowHeaders)
	}
	if decision.exposeHeaders != "" {
		header.Set("Access-Control-Expose-Headers", decision.exposeHeaders)
	}
	if decision.maxAge > 0 {
		header.Set("Access-Control-Max-Age", strconv.Itoa(decision.maxAge))
	}
}

func (w *corsResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *corsResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(src)
	}
	return io.Copy(w.ResponseWriter, src)
}

func (w *corsResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *corsResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// splitHeaderList parses a comma separated header list (as used by
// Access-Control-Request-Headers) into trimmed, non-empty items.
func splitHeaderList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// corsHandler answers preflight requests, serves the `?cors` subresource and
// adds the resolved CORS headers to every other response.
func corsHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		bucket := bucketOnlyName(r.URL.Path)
		rules := corsRulesForBucket(bucket)

		if r.Method == http.MethodOptions {
			// A preflight carries no credentials by design, so it is answered here,
			// before the authentication middleware can reject it.
			if origin != "" {
				answerS3CORSPreflight(w, r, origin, rules)
				return
			}
		}

		// The `?cors` subresource is answered before gofakes3 sees the request, so
		// its responses (including their errors) have to carry the CORS headers
		// too, otherwise a browser cannot read them. Its own method does not have
		// to be listed in the rule: the rule describes what object requests may do.
		if isS3CORSConfigurationRequest(r) {
			var decision s3CORSDecision
			if origin != "" {
				decision = decideS3CORS(rules, origin, "", nil)
			}
			handleS3CORSConfiguration(&corsResponseWriter{ResponseWriter: w, decision: decision}, r)
			return
		}

		if origin == "" {
			// Not a browser cross-origin request: leave the response untouched.
			next.ServeHTTP(w, r)
			return
		}

		decision := decideS3CORS(rules, origin, r.Method, nil)
		next.ServeHTTP(&corsResponseWriter{ResponseWriter: w, decision: decision}, r)
	})
}

// isS3CORSConfigurationRequest reports whether r addresses the `?cors`
// subresource with a method the gateway implements.
func isS3CORSConfigurationRequest(r *http.Request) bool {
	if _, ok := r.URL.Query()["cors"]; !ok {
		return false
	}
	return r.Method == http.MethodGet || r.Method == http.MethodPut || r.Method == http.MethodDelete
}

// answerS3CORSPreflight answers an OPTIONS request without touching the signed
// request path: browsers never sign a preflight, so it must not reach the auth
// middleware.
func answerS3CORSPreflight(w http.ResponseWriter, r *http.Request, origin string, rules []s3CORSRule) {
	requestedMethod := strings.ToUpper(strings.TrimSpace(r.Header.Get("Access-Control-Request-Method")))
	requestedHeaders := splitHeaderList(r.Header.Get("Access-Control-Request-Headers"))

	decision := decideS3CORS(rules, origin, requestedMethod, requestedHeaders)
	if !decision.allowed() {
		// Even a rejected preflight must not be cached as if it had no rules.
		w.Header().Add("Vary", "Origin")
		w.Header().Add("Vary", "Access-Control-Request-Method")
		w.Header().Add("Vary", "Access-Control-Request-Headers")
		w.WriteHeader(http.StatusForbidden)
		return
	}

	// The answer depends on all three request headers, so caches must not
	// reuse it for a different origin/method/header set.
	w.Header().Set("Vary", "Origin, Access-Control-Request-Method, Access-Control-Request-Headers")
	w.Header().Set("Access-Control-Allow-Origin", decision.origin)
	w.Header().Set("Access-Control-Allow-Methods", decision.allowMethods)
	if decision.allowHeaders != "" {
		w.Header().Set("Access-Control-Allow-Headers", decision.allowHeaders)
	}
	if decision.exposeHeaders != "" {
		w.Header().Set("Access-Control-Expose-Headers", decision.exposeHeaders)
	}
	w.Header().Set("Access-Control-Max-Age", strconv.Itoa(decision.maxAge))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// handleS3CORSConfiguration serves the `?cors` subresource. It returns true
// when the request was a CORS configuration request and has been answered.
func handleS3CORSConfiguration(w http.ResponseWriter, r *http.Request) bool {
	query := r.URL.Query()
	if _, ok := query["cors"]; !ok {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		// OPTIONS and the rest fall through to the normal handlers.
		return false
	}

	bucket := bucketOnlyName(r.URL.Path)
	if bucket == "" {
		return false
	}
	if _, err := getBucketByName(bucket); err != nil {
		writeS3Error(w, r, http.StatusNotFound, "NoSuchBucket", bucket,
			"The specified bucket does not exist.")
		return true
	}

	write := r.Method != http.MethodGet
	if status, code, message := s3CORSAPIAuthorized(r, bucket, write); code != "" {
		writeS3Error(w, r, status, code, bucket, message)
		return true
	}

	switch r.Method {
	case http.MethodGet:
		rules, ok := getS3CORSConfiguration()[bucket]
		if !ok || len(rules) == 0 {
			writeS3Error(w, r, http.StatusNotFound, "NoSuchCORSConfiguration", bucket,
				"The CORS configuration does not exist.")
			return true
		}
		putS3CORSConfiguration(w, r, rules)
	case http.MethodPut:
		rules, err := readS3CORSConfiguration(w, r)
		if err != nil {
			writeS3Error(w, r, http.StatusBadRequest, "MalformedXML", bucket, err.Error())
			return true
		}
		if err := saveS3CORSRules(bucket, rules); err != nil {
			log.Errorf("failed to save CORS configuration for bucket %q: %v", bucket, err)
			writeS3Error(w, r, http.StatusInternalServerError, "InternalError", bucket,
				"We encountered an internal error. Please try again.")
			return true
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if err := saveS3CORSRules(bucket, nil); err != nil {
			log.Errorf("failed to delete CORS configuration for bucket %q: %v", bucket, err)
			writeS3Error(w, r, http.StatusInternalServerError, "InternalError", bucket,
				"We encountered an internal error. Please try again.")
			return true
		}
		w.WriteHeader(http.StatusNoContent)
	}
	return true
}

// s3CORSAPIAuthorized authorises a CORS configuration request. Reads need only
// bucket access; writes additionally need a key that may write to the bucket.
// signatureV4Prefix identifies an AWS Signature Version 4 Authorization header.
const signatureV4Prefix = "AWS4-HMAC-SHA256 "

// s3CORSAPIAuthorized verifies a `?cors` request and returns the S3 error to
// answer with when it must be refused ("" code means allowed).
//
// gofakes3 verifies request signatures inside its own handler, but `?cors` is
// answered before that handler runs, so the signature has to be checked here as
// well. Matching the access key ID alone is not enough: the ID travels in clear
// text in every request, so trusting it would let anybody who knows it rewrite
// another bucket's CORS policy.
func s3CORSAPIAuthorized(r *http.Request, bucket string, write bool) (int, string, string) {
	rules := getS3BucketAuthRules()
	if len(rules) == 0 {
		// No credentials configured: the gateway is open, exactly as it is for
		// object requests.
		return 0, "", ""
	}

	// An unsigned call is refused before the signature check so the answer is the
	// usual `AccessDenied` rather than a credential error.
	authorization := r.Header.Get("Authorization")
	query := r.URL.Query()
	if authorization == "" && query.Get("X-Amz-Signature") == "" && query.Get("AWSAccessKeyId") == "" {
		return http.StatusForbidden, "AccessDenied", "Access Denied."
	}

	secrets := make(map[string]string, len(rules))
	for _, rule := range rules {
		secrets[rule.AccessKey] = rule.SecretKey
	}
	lookup := func(accessKey string) (string, bool) {
		secret, ok := secrets[accessKey]
		return secret, ok
	}
	var errCode signature.ErrorCode
	if strings.HasPrefix(authorization, signatureV4Prefix) || query.Get("X-Amz-Signature") != "" {
		errCode = signature.V4SignVerifyWithLookup(r, lookup)
	} else {
		errCode = signature.V2SignVerifyWithLookup(r, lookup)
	}
	if errCode != signature.ErrNone {
		apiErr := signature.GetAPIError(errCode)
		status := apiErr.HTTPStatusCode
		if status == 0 {
			status = http.StatusForbidden
		}
		return status, apiErr.Code, apiErr.Description
	}

	if !s3BucketAuthorized(r, bucket) || (write && !s3BucketCanWrite(r)) {
		return http.StatusForbidden, "AccessDenied", "Access Denied."
	}
	return 0, "", ""
}

// readS3CORSConfiguration parses and validates a request body.
func readS3CORSConfiguration(w http.ResponseWriter, r *http.Request) ([]s3CORSRule, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, s3CORSBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > s3CORSBodyLimit {
		return nil, errors.New("the CORS configuration is too large")
	}
	var config s3CORSConfiguration
	if err := xml.Unmarshal(body, &config); err != nil {
		return nil, errors.New("the XML you provided was not well-formed or did not validate against our published schema")
	}
	if err := validateS3CORSRules(config.Rules); err != nil {
		return nil, err
	}
	return config.Rules, nil
}

// putS3CORSConfiguration writes the stored configuration as XML.
func putS3CORSConfiguration(w http.ResponseWriter, r *http.Request, rules []s3CORSRule) {
	payload, err := xml.MarshalIndent(s3CORSConfiguration{XMLNS: s3CORSXMLNS, Rules: rules}, "", "  ")
	if err != nil {
		writeS3Error(w, r, http.StatusInternalServerError, "InternalError", bucketOnlyName(r.URL.Path),
			"We encountered an internal error. Please try again.")
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(payload)
}

// saveS3CORSRules replaces (or with nil removes) the rules of one bucket.
func saveS3CORSRules(bucket string, rules []s3CORSRule) error {
	corsSettingMu.Lock()
	defer corsSettingMu.Unlock()

	config := getS3CORSConfiguration()
	if len(rules) == 0 {
		delete(config, bucket)
	} else {
		config[bucket] = rules
	}
	return saveS3CORSSetting(config)
}

// validateS3CORSRules applies S3's schema checks and normalises the document in
// place (trimmed values, upper-cased methods, de-duplicated lists).
func validateS3CORSRules(rules []s3CORSRule) error {
	if len(rules) == 0 {
		return errors.New("the XML you provided was not well-formed or did not validate against our published schema")
	}
	if len(rules) > s3CORSMaxRules {
		return fmt.Errorf("the CORS configuration may not contain more than %d rules", s3CORSMaxRules)
	}
	for i := range rules {
		rule := &rules[i]
		if normalized := appendUniqueFold(nil, rule.AllowedOrigins...); len(normalized) == 0 {
			return errors.New("a CORSRule must have at least one AllowedOrigin")
		} else {
			rule.AllowedOrigins = normalized
		}

		methods := make([]string, 0, len(rule.AllowedMethods))
		for _, method := range rule.AllowedMethods {
			normalized := strings.ToUpper(strings.TrimSpace(method))
			switch normalized {
			case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPost, http.MethodDelete:
				methods = appendUniqueFold(methods, normalized)
			default:
				if normalized != "" {
					return fmt.Errorf("invalid AllowedMethod %q: must be one of GET, HEAD, PUT, POST, DELETE", method)
				}
			}
		}
		if len(methods) == 0 {
			return errors.New("a CORSRule must have at least one AllowedMethod")
		}
		rule.AllowedMethods = methods

		rule.AllowedHeaders = appendUniqueFold(nil, rule.AllowedHeaders...)
		rule.ExposeHeaders = appendUniqueFold(nil, rule.ExposeHeaders...)
		if rule.MaxAgeSeconds < 0 {
			return fmt.Errorf("invalid MaxAgeSeconds %d: must not be negative", rule.MaxAgeSeconds)
		}
	}
	return nil
}
