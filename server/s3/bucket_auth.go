package s3

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	log "github.com/sirupsen/logrus"
)

type s3ContextKey string

const (
	s3AuthRuleContextKey s3ContextKey = "s3_bucket_auth_rule"
)

// S3BucketAuthRule defines permissions for a specific AccessKey
type S3BucketAuthRule struct {
	AccessKey string   `json:"access_key_id"`
	SecretKey string   `json:"secret_access_key"`
	Buckets   []string `json:"buckets"`
	ReadOnly  bool     `json:"readonly"`

	// writableBuckets records the write permission per bucket, and is only set
	// when one access key appears in several rules whose read-only flags
	// disagree. ReadOnly is a single flag for the whole rule, so taking it from
	// either row made every bucket inherit that row's setting: a key that was
	// read-only on one bucket silently lost writes on all the buckets the admin
	// had marked writable. Not part of the JSON document.
	writableBuckets map[string]bool
}

// UnmarshalJSON accepts both the canonical access_key_id / secret_access_key
// spelling and the older access_key / secret_key one, and the read-only flag as
// `readonly`, `read_only` or `readOnly`. The setting is plain JSON edited by
// hand as often as by the admin UI, and encoding/json ignores unknown members:
// a misspelt flag would silently turn a read-only key into a read-write one, so
// every plausible spelling is accepted instead.
func (r *S3BucketAuthRule) UnmarshalJSON(data []byte) error {
	var raw struct {
		AccessKeyID     string   `json:"access_key_id"`
		SecretAccessKey string   `json:"secret_access_key"`
		AccessKey       string   `json:"access_key"`
		SecretKey       string   `json:"secret_key"`
		Buckets         []string `json:"buckets"`
		ReadOnly        *bool    `json:"readonly"`
		ReadOnlySnake   *bool    `json:"read_only"`
		ReadOnlyCamel   *bool    `json:"readOnly"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.AccessKey = raw.AccessKeyID
	if r.AccessKey == "" {
		r.AccessKey = raw.AccessKey
	}
	r.SecretKey = raw.SecretAccessKey
	if r.SecretKey == "" {
		r.SecretKey = raw.SecretKey
	}
	r.Buckets = raw.Buckets
	r.ReadOnly = false
	for _, flag := range []*bool{raw.ReadOnly, raw.ReadOnlySnake, raw.ReadOnlyCamel} {
		if flag != nil {
			r.ReadOnly = *flag
			break
		}
	}
	return nil
}

// MatchesBucket checks whether the rule allows access to the given bucket name
func (r *S3BucketAuthRule) MatchesBucket(bucket string) bool {
	if r == nil {
		return false
	}
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return false
	}
	for _, b := range r.Buckets {
		b = strings.TrimSpace(b)
		if b == "*" || b == bucket {
			return true
		}
		if strings.HasSuffix(b, "*") {
			prefix := strings.TrimSuffix(b, "*")
			if strings.HasPrefix(bucket, prefix) {
				return true
			}
		}
	}
	return false
}

// CanWrite returns whether the rule allows write operations (PUT/POST/DELETE)
// on at least one of its buckets.
func (r *S3BucketAuthRule) CanWrite() bool {
	if r == nil {
		return false
	}
	return !r.ReadOnly
}

// CanWriteBucket returns whether the rule allows writes on this bucket. Rules
// merged from several rows can carry per-bucket read-only settings, so callers
// that know the bucket must use this instead of CanWrite.
func (r *S3BucketAuthRule) CanWriteBucket(bucket string) bool {
	if r == nil {
		return false
	}
	if r.writableBuckets != nil {
		if writable, ok := r.writableBuckets[bucket]; ok {
			return writable
		}
		return false
	}
	return !r.ReadOnly
}

// getS3BucketAuthRules loads rules from s3_bucket_auth setting,
// falling back to legacy s3_access_key_id / s3_secret_access_key if not configured.
var (
	badAuthRulesMu   sync.Mutex
	badAuthRulesSeen string
)

// warnBadAuthRulesOnce reports true only for a malformed value it has not
// already complained about.
// minS3AccessKeyLength mirrors gofakes3's own limit (signature.accessKeyMinLen):
// a shorter credential is rejected while parsing the signature, so a rule for
// such a key can never authenticate anything.
const minS3AccessKeyLength = 3

func warnBadAuthRulesOnce(raw string) bool {
	badAuthRulesMu.Lock()
	defer badAuthRulesMu.Unlock()
	if badAuthRulesSeen == raw {
		return false
	}
	badAuthRulesSeen = raw
	return true
}

// getS3BucketAuthRules returns the rules that can authenticate a request,
// ignoring a gateway whose credentials are set but unusable (see
// loadS3BucketAuthRules for the difference).
func getS3BucketAuthRules() []S3BucketAuthRule {
	rules, _ := loadS3BucketAuthRules()
	return rules
}

// authMisconfigured reports whether credentials are configured but yielded
// nothing usable. Callers that would otherwise treat "no key" as the documented
// open mode must check this first and refuse.
func authMisconfigured() bool {
	_, misconfigured := loadS3BucketAuthRules()
	return misconfigured
}

// loadS3BucketAuthRules parses the configured access keys.
//
// A gateway with no credentials configured at all stays open, which is
// upstream's behaviour and what a deployment that never set up S3 expects. A
// gateway whose credentials are configured but unusable is a different matter:
// the setting is present, the operator believes it is protecting the buckets,
// and every rule was rejected. Serving such a request as anonymous access would
// hand out every bucket because of a typo, so it is reported as misconfigured
// and the callers refuse the request instead.
func loadS3BucketAuthRules() (rules []S3BucketAuthRule, misconfigured bool) {
	return loadS3BucketAuthRulesFrom(
		setting.GetStr(conf.S3BucketAuth),
		setting.GetStr(conf.S3AccessKeyId),
		setting.GetStr(conf.S3SecretAccessKey),
	)
}

// loadS3BucketAuthRulesFrom is loadS3BucketAuthRules on a set of raw setting
// values instead of the live ones. The key ring needs it: it compares a
// fingerprint of the settings and only then parses them, and if the two reads
// are separate, a settings change in between produces keys that do not belong
// to the fingerprint. That state is not recoverable by waiting, because the
// ring remembers the fingerprint it has already applied.
func loadS3BucketAuthRulesFrom(rawVal, akVal, skVal string) (rules []S3BucketAuthRule, misconfigured bool) {
	raw := strings.TrimSpace(rawVal)
	ak := strings.TrimSpace(akVal)
	sk := strings.TrimSpace(skVal)
	// Parsed once per distinct configuration: this runs for every request,
	// including anonymous ones, so re-unmarshalling a large rule set per request
	// is work an unauthenticated client can demand at will.
	res, _ := authRulesCache.get(raw+"\x00"+ak+"\x00"+sk, func(string) (authRulesResult, error) {
		rules, misconfigured := parseS3BucketAuthRules(raw, ak, sk)
		return authRulesResult{rules: rules, misconfigured: misconfigured}, nil
	})
	return res.rules, res.misconfigured
}

// authRulesResult is what one configuration parses to.
type authRulesResult struct {
	rules         []S3BucketAuthRule
	misconfigured bool
}

var authRulesCache parsedSettingCache[authRulesResult]

// parseS3BucketAuthRules turns the three credential settings into usable rules.
func parseS3BucketAuthRules(raw, ak, sk string) (rules []S3BucketAuthRule, misconfigured bool) {
	var parsed []S3BucketAuthRule
	parseFailed := false
	if raw != "" && raw != "[]" {
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			// The value itself must never reach the log: it holds a
			// secret_access_key per rule, and any anonymous request can be what
			// triggers this line. The error carries the offset, which is what
			// an admin needs, and the length tells whether the payload is even
			// plausible.
			log.Warnf("Failed to parse %s as JSON (%d bytes): %v", conf.S3BucketAuth, len(raw), err)
			parsed = nil
			parseFailed = true
		}
	}

	// A rule with no access key can never authenticate a request. Keeping it
	// around would only flip the gateway into "credentials are configured"
	// mode and lock out every anonymous client, so such rules are dropped.
	//
	// Rules that repeat an access key are merged into one (union of buckets, and
	// readonly when ANY duplicate is readonly: a hand-edited or half-migrated
	// list must not silently widen a key the operator marked read-only). Every
	// lookup below stops
	// at the first rule carrying the key, so a second rule for the same key
	// would shadow the rest: the admin UI used to write one row per bucket even
	// when every row carried the master key, and in that shape the key could
	// reach only the first bucket listed and got 403 on all the others.
	rules = make([]S3BucketAuthRule, 0, len(parsed)+1)
	byKey := make(map[string]int, len(parsed)+1)
	for _, rule := range parsed {
		key := strings.TrimSpace(rule.AccessKey)
		if key == "" {
			continue
		}
		if len(key) < minS3AccessKeyLength {
			// gofakes3 refuses to parse a credential whose access key is shorter
			// than three characters, so such a rule could never authenticate
			// anything. Keeping it only produced a permanent 403 that looked like
			// a missing key.
			if warnBadAuthRulesOnce(key) {
				log.Warnf("Dropping the rule for access key %q: an access key needs at least %d characters",
					key, minS3AccessKeyLength)
			}
			continue
		}
		rule.AccessKey = key
		if at, ok := byKey[key]; ok {
			prev := rules[at]
			rules[at].Buckets = mergeBucketLists(prev.Buckets, rule.Buckets)
			wildcard := hasWildcardBucket(prev.Buckets) || hasWildcardBucket(rule.Buckets)
			// The map has to be rebuilt for every later row, not only when the
			// two flags differ. Once a row has created it, ReadOnly is false
			// (the merge ANDs the two flags), so a third row carrying another
			// bucket with readonly=false compared equal, the merge was skipped,
			// and that bucket stayed absent from the map - CanWriteBucket then
			// answered false for a bucket the operator had explicitly left
			// writable. Rows (a) readonly, (b) and (c) writable locked writes
			// out of (c).
			if !wildcard && (prev.ReadOnly != rule.ReadOnly || prev.writableBuckets != nil) {
				// Keep both rows' intent: a bucket is writable when either row
				// says so, instead of letting one row's flag win for all of them.
				rules[at].writableBuckets = mergeWritableBuckets(prev, rule)
			}
			if wildcard {
				// A pattern cannot be resolved bucket by bucket, so the
				// restrictive reading wins: "*" marked read-only means read-only.
				// Any per-bucket writability left over from an earlier row of the
				// same key would contradict that, so it is dropped.
				rules[at].ReadOnly = prev.ReadOnly || rule.ReadOnly
				rules[at].writableBuckets = nil
			} else {
				rules[at].ReadOnly = prev.ReadOnly && rule.ReadOnly
			}
			if rules[at].SecretKey == "" {
				rules[at].SecretKey = rule.SecretKey
			}
			continue
		}
		byKey[key] = len(rules)
		rules = append(rules, rule)
	}

	// A rule without a secret is as useless as one without a key, and worse: an
	// empty secret is a working signing key (the HMAC chain is keyed with ""),
	// so anyone who knows the access key id - which is public, it travels in
	// every request and every presigned URL - could sign as that key. Rules are
	// therefore dropped when either half is empty.
	kept := rules[:0]
	for _, rule := range rules {
		if strings.TrimSpace(rule.SecretKey) == "" {
			if warnBadAuthRulesOnce(raw) {
				log.Warnf("Dropping the rule for access key %q: it has no secret_access_key", rule.AccessKey)
			}
			continue
		}
		rule.SecretKey = strings.TrimSpace(rule.SecretKey)
		kept = append(kept, rule)
	}
	rules = kept

	// The legacy global key keeps working after per-bucket rules are added, and
	// it always means "every bucket, read/write": that is the contract the
	// admin UI states, and it is also the safety net for a per-bucket row that
	// was saved with the master key by mistake. It is kept first so a
	// per-bucket rule reusing that key cannot shadow it.
	// Either half counts as "the operator configured credentials". Only looking
	// at the access key id let a lone secret_access_key (the halfway state while
	// the pair is being saved, or after the id is cleared) be read as "nothing
	// configured", which handed every bucket to anonymous clients through the
	// key-less open mode. A half-configured pair is reported as misconfigured,
	// which callers refuse, so the gateway fails closed instead.
	legacyConfigured := ak != "" || sk != ""
	if ak == "" && sk != "" {
		if warnBadAuthRulesOnce(sk) {
			log.Warnf("%s is set but %s is empty: refusing to serve the buckets until the pair is complete",
				conf.S3SecretAccessKey, conf.S3AccessKeyId)
		}
	} else if legacyConfigured && sk == "" {
		if warnBadAuthRulesOnce(ak) {
			log.Warnf("%s is set but %s is empty: refusing to use the key", conf.S3AccessKeyId, conf.S3SecretAccessKey)
		}
	} else if legacyConfigured {
		global := S3BucketAuthRule{
			AccessKey: ak,
			SecretKey: sk,
			Buckets:   []string{"*"},
		}
		if at, ok := byKey[ak]; ok {
			// The master key is documented as the all-buckets key, so its reach
			// stays "*", but a readonly flag written on a per-bucket rule for the
			// same key must not be silently dropped: widening a read-only key into
			// a writable master key is exactly the mistake an admin would not see.
			if rules[at].ReadOnly {
				global.ReadOnly = true
				log.Warnf("The master access key %q is also listed as a read-only bucket rule; it stays read-only everywhere", ak)
			}
			if at == 0 {
				rules[0] = global
			} else {
				rules = append(rules[:at], rules[at+1:]...)
				rules = append([]S3BucketAuthRule{global}, rules...)
			}
		} else {
			rules = append([]S3BucketAuthRule{global}, rules...)
		}
	}

	// Credentials were configured (per-bucket rules or the legacy key) but
	// nothing usable came out of it. Callers must refuse the request instead of
	// falling through to the anonymous path, which serves every bucket.
	configured := parseFailed || (raw != "" && raw != "[]") || legacyConfigured
	return rules, configured && len(rules) == 0
}

// mergeBucketLists unions two bucket lists, keeping first-seen order. A "*" on
// either side wins outright: it already covers everything else.
// hasWildcardBucket reports whether any entry is a wildcard ("*" or "prefix*").
// Rules that span wildcards keep the plain whole-rule read-only flag: a
// per-bucket view cannot be matched against a pattern.
func hasWildcardBucket(buckets []string) bool {
	for _, bucket := range buckets {
		if strings.Contains(bucket, "*") {
			return true
		}
	}
	return false
}

// mergeWritableBuckets unions two rows' write scopes for the same access key.
func mergeWritableBuckets(prev, next S3BucketAuthRule) map[string]bool {
	out := make(map[string]bool, len(prev.Buckets)+len(next.Buckets))
	if prev.writableBuckets != nil {
		for bucket, writable := range prev.writableBuckets {
			out[bucket] = writable
		}
	} else {
		for _, bucket := range prev.Buckets {
			out[strings.TrimSpace(bucket)] = !prev.ReadOnly
		}
	}
	for _, bucket := range next.Buckets {
		bucket = strings.TrimSpace(bucket)
		// Either row granting writes wins.
		if writable, ok := out[bucket]; !ok || !writable {
			out[bucket] = !next.ReadOnly
		}
	}
	delete(out, "")
	return out
}

func mergeBucketLists(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, v := range list {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if v == "*" {
				return []string{"*"}
			}
			if seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// resolveAuthPairs returns map[accessKey]secretKey for gofakes3 WithV4Auth.
func resolveAuthPairs() map[string]string {
	return resolveAuthPairsLogging(true)
}

// resolveAuthPairsQuiet is the same lookup without the diagnostics, for the
// background re-read that runs every couple of seconds.
func resolveAuthPairsQuiet() map[string]string {
	return resolveAuthPairsLogging(false)
}

func resolveAuthPairsLogging(logProblems bool) map[string]string {
	return authPairsFromRules(getS3BucketAuthRules(), logProblems)
}

// authPairsFromRules turns resolved rules into the accessKey->secretKey map
// gofakes3 verifies signatures against, without going back to the settings.
func authPairsFromRules(rules []S3BucketAuthRule, logProblems bool) map[string]string {
	if len(rules) == 0 {
		return nil
	}
	pairs := make(map[string]string, len(rules))
	for _, r := range rules {
		if r.AccessKey == "" {
			if logProblems {
				log.Warnf("S3 bucket auth: ignoring a rule without access_key_id (buckets=%v)", r.Buckets)
			}
			continue
		}
		pairs[r.AccessKey] = r.SecretKey
	}
	if len(pairs) == 0 && logProblems {
		// gofakes3 opens up completely when it has no keys at all, and the
		// request-level checks below can then only reject what it manages to
		// identify. Say so loudly instead of failing open in silence.
		log.Errorf("S3 bucket auth: %d rule(s) configured but none has an access_key_id; S3 signature verification is DISABLED until this is fixed", len(rules))
	}
	return pairs
}

// v4CredentialAccessKey pulls the access key out of a V4 credential of the
// form "<accessKey>/<date>/<region>/<service>/aws4_request".
//
// The scope is always the last four components, so anything in front of them is
// part of the key. Keys generated by this admin UI contain a slash, and taking
// the first component (as the AWS examples suggest) silently truncates them to
// a key nobody is configured with - which surfaces to clients as
// InvalidAccessKeyId, not as a permission problem.
func v4CredentialAccessKey(cred string) string {
	cred = strings.TrimSpace(cred)
	if cred == "" {
		return ""
	}
	parts := strings.Split(cred, "/")
	// Only strip the scope when the tail really is one. Truncating an arbitrary
	// string to its first segment would let a garbled credential such as
	// "admin/x" match the configured "admin" key.
	if len(parts) >= 5 && parts[len(parts)-1] == "aws4_request" {
		return strings.TrimSpace(strings.Join(parts[:len(parts)-4], "/"))
	}
	return strings.TrimSpace(cred)
}

// accessKeyFromAuthHeader pulls the access key out of an Authorization header.
func accessKeyFromAuthHeader(auth string) string {
	if auth == "" {
		return ""
	}
	// AWS V4: "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=..."
	if idx := strings.Index(auth, "Credential="); idx != -1 {
		credStr := auth[idx+len("Credential="):]
		if commaIdx := strings.Index(credStr, ","); commaIdx != -1 {
			credStr = credStr[:commaIdx]
		}
		return v4CredentialAccessKey(credStr)
	}
	// AWS V2: "AWS AKIAIOSFODNN7EXAMPLE:signature". gofakes3 removes every space
	// before it looks for the "AWS" prefix, so "AWSKEY:sig" and "AWS KEY:sig"
	// verify against the same key; this parser has to read them the same way or
	// the verifier and the authorizer end up using different identities.
	compact := strings.ReplaceAll(auth, " ", "")
	if strings.HasPrefix(compact, "AWS") {
		parts := strings.SplitN(strings.TrimPrefix(compact, "AWS"), ":", 2)
		return strings.TrimSpace(parts[0])
	}
	return ""
}

// accessKeyFromQuery pulls the access key out of a presigned URL.
//
// queryCredentialConflict reports whether the two query spellings name different
// keys. gofakes3's V2 parser prefers AWSAccessKeyId while this one used to
// prefer X-Amz-Credential, so a URL carrying both was verified as one key and
// authorised as another.
func accessKeyFromQuery(query url.Values) (key string, conflict bool) {
	v4 := ""
	if cred := query.Get("X-Amz-Credential"); cred != "" {
		v4 = v4CredentialAccessKey(cred)
	}
	v2 := strings.TrimSpace(query.Get("AWSAccessKeyId"))
	if v4 != "" && v2 != "" && v4 != v2 {
		return "", true
	}
	if v4 != "" {
		return v4, false
	}
	return v2, false
}

// extractAccessKey extracts the access key the request was signed with.
//
// The Authorization header wins and the query is only read when it is absent,
// which is the order gofakes3's verifier uses. Reading the query first made the
// two disagree: a request signed with one key ("Authorization: AWS low:...")
// while carrying a presigned credential for another ("?X-Amz-Credential=victim/...")
// was authorized as the victim key and checked against the other signature.
func extractAccessKey(r *http.Request) string {
	ak, _ := requestAccessKey(r)
	return ak
}

// requestAccessKey resolves the access key of a request once, for both
// authentication and authorisation.
//
// ok is false when the request must be refused outright: credentials in both
// places, an Authorization header that cannot be parsed, or a query naming two
// different keys. Every such shape is a request whose identity is ambiguous,
// and the layers disagreeing about it is exactly how a per-bucket key could be
// authorised as the master key.
func requestAccessKey(r *http.Request) (key string, ok bool) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	headerKey := accessKeyFromAuthHeader(auth)
	queryKey, queryConflict := accessKeyFromQuery(r.URL.Query())
	switch {
	case queryConflict:
		return "", false
	case auth != "" && headerKey == "":
		return "", false
	case headerKey != "" && queryKey != "":
		return "", false
	case headerKey != "":
		return headerKey, true
	default:
		return queryKey, true
	}
}

// hasConflictingCredentials reports whether the request carries credentials in
// both the header and the query. No client does that, and every request that
// mixes two identities is either broken or an attempt to be authorized as one
// key while being verified as another.
func hasConflictingCredentials(r *http.Request) bool {
	_, ok := requestAccessKey(r)
	return !ok
}

// copySourceRef parses X-Amz-Copy-Source the way gofakes3 does: the leading
// slash is optional, the first segment names the source bucket and the rest,
// minus any "?versionId" suffix, is the percent-encoded key.
//
// present is false when the header is absent. bucket is empty when the value is
// malformed - gofakes3 then indexes parts[1] without checking and panics, so
// such a request has to be rejected before it reaches that code.
func copySourceRef(r *http.Request) (bucket, key string, present bool) {
	values, has := r.Header[http.CanonicalHeaderKey("X-Amz-Copy-Source")]
	raw := ""
	if has && len(values) > 0 {
		raw = strings.TrimSpace(values[0])
	}
	if !has {
		return "", "", false
	}
	if raw == "" {
		// The header exists but carries nothing. gofakes3 decides whether to copy
		// by the header's presence alone and then indexes the parsed source as
		// parts[1], so this shape reached it as an index-out-of-range panic. An
		// empty source is malformed: report it so the caller answers 400.
		return "", "", true
	}
	// A value whose separators are percent-encoded ("bucket%2Fkey") is refused
	// rather than decoded: gofakes3 parses the same header itself with a plain
	// split on "/", so accepting it here only moved the failure deeper — it
	// indexed parts[1] on a one-element slice and panicked into a 500. Reporting
	// the malformed value keeps a bad header a 400.
	parts := strings.SplitN(strings.TrimPrefix(raw, "/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", "", true
	}
	key = strings.SplitN(parts[1], "?", 2)[0]
	if decoded, err := url.QueryUnescape(key); err == nil {
		key = decoded
	}
	if key == "" {
		return "", "", true
	}
	return parts[0], key, true
}

// copySourceIsSigned reports whether the copy source this request carries is
// covered by its signature.
//
// Signature V4 only authenticates the headers named in SignedHeaders: gofakes3
// canonicalises exactly those and compares the result, so a header that is
// merely present is not covered - appending `X-Amz-Copy-Source` to a signed
// request leaves the signature valid. A presigned URL and a header-signed
// request are equally exposed (X-Amz-SignedHeaders is `host` for most presigned
// links, and a client is free to sign a shorter list than it sends), which is
// why both spellings are read here. AWS refuses such a request with "There were
// headers present in the request which were not signed".
//
// Signature V2 is different: it hashes every x-amz- header into the string to
// sign, so a V2 request cannot carry one the signer did not see. A request with
// no credential at all has nothing to cover the header with either; the bucket
// rules alone decide what an anonymous caller may do.
func copySourceIsSigned(r *http.Request) bool {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	var signed string
	switch {
	case auth != "" && !strings.HasPrefix(auth, "AWS4-"):
		return true
	case auth != "":
		signed = authorizationSignedHeaders(auth)
		if signed == "" {
			// The signed-header list could not be read, so there is nothing to
			// vouch for the copy source with.
			return false
		}
	case r.URL.Query().Get("X-Amz-Signature") != "":
		signed = r.URL.Query().Get("X-Amz-SignedHeaders")
	default:
		return true
	}

	signedSet := make(map[string]bool)
	for _, name := range strings.Split(strings.ToLower(signed), ";") {
		signedSet[strings.TrimSpace(name)] = true
	}
	// Every copy header the request actually carries has to be listed, not just
	// the source: `X-Amz-Copy-Source-Range` steers which slice of the source is
	// read, so an unsigned one would let a replayed UploadPartCopy take a
	// different part of an object than the one that was signed.
	for name := range r.Header {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "x-amz-copy-source") {
			continue
		}
		if !signedSet[lower] {
			return false
		}
	}
	return true
}

// authorizationSignedHeaders returns the SignedHeaders list of a Signature V4
// Authorization header ("AWS4-HMAC-SHA256 Credential=..., SignedHeaders=...,
// Signature=..."), or "" when the header does not carry one.
func authorizationSignedHeaders(auth string) string {
	const prefix = "SignedHeaders="
	for _, field := range strings.Split(auth, ",") {
		field = strings.TrimSpace(field)
		if len(field) < len(prefix) || !strings.EqualFold(field[:len(prefix)], prefix) {
			continue
		}
		return field[len(prefix):]
	}
	return ""
}

type s3AccessDeniedError struct {
	XMLName    xml.Name `xml:"Error"`
	Code       string   `xml:"Code"`
	Message    string   `xml:"Message"`
	BucketName string   `xml:"BucketName,omitempty"`
	Resource   string   `xml:"Resource,omitempty"`
}

func writeS3Error(w http.ResponseWriter, r *http.Request, status int, code, bucket, msg string) {
	errResp := s3AccessDeniedError{
		Code:       code,
		Message:    msg,
		BucketName: bucket,
		Resource:   r.URL.Path,
	}
	payload, err := xml.MarshalIndent(errResp, "", "  ")
	if err != nil {
		http.Error(w, code, status)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(payload)
}

func writeAccessDenied(w http.ResponseWriter, r *http.Request, bucket, msg string) {
	if msg == "" {
		msg = "Access Denied"
	}
	writeS3Error(w, r, http.StatusForbidden, "AccessDenied", bucket, msg)
}

func writeInvalidAccessKey(w http.ResponseWriter, r *http.Request) {
	writeS3Error(w, r, http.StatusForbidden, "InvalidAccessKeyId", "",
		"The AWS Access Key Id you provided does not exist in our records.")
}

// bucketOnlyName returns the bucket segment of a request path that addresses a
// bucket without an object key (GET /bucket -> ListObjects). parseObjectPath
// deliberately rejects those paths, so bucket-level requests need their own
// parser; without it a key scoped to one bucket could list any other bucket.
func bucketOnlyName(rawPath string) string {
	trimmed := strings.Trim(strings.TrimPrefix(rawPath, "/"), "/")
	if trimmed == "" {
		return ""
	}
	if idx := strings.Index(trimmed, "/"); idx != -1 {
		return trimmed[:idx]
	}
	return trimmed
}

func isReadOnlyMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// bucketAuthHandler intercepts requests and enforces bucket-level access control.
//
// publicHandler serves requests that reached a public bucket without any
// credentials; it is backed by a gofakes3 instance without V4 keys and must
// therefore only ever see anonymous reads of public buckets.
func bucketAuthHandler(next http.Handler, publicHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rules, misconfigured := loadS3BucketAuthRules()
		if misconfigured {
			// Credentials are configured but nothing usable came out of them
			// (broken JSON, a rule without a secret, a legacy key without one).
			// The anonymous path below would serve every bucket, so the gateway
			// refuses instead and says why.
			log.Errorf("S3 gateway is refusing every request: %s/%s are set but yield no usable access key",
				conf.S3BucketAuth, conf.S3AccessKeyId)
			writeS3Error(w, r, http.StatusServiceUnavailable, "ServiceUnavailable",
				bucketOnlyName(r.URL.Path), "The S3 credentials of this server are misconfigured")
			return
		}
		if len(rules) == 0 {
			// No key configured at all: keep upstream's anonymous, fully open
			// behaviour for deployments that never set up S3 credentials.
			next.ServeHTTP(w, r)
			return
		}

		if hasConflictingCredentials(r) {
			writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument",
				bucketOnlyName(r.URL.Path), "Credentials must be supplied either in the Authorization header or in the query string, not both")
			return
		}

		ak := extractAccessKey(r)
		if ak == "" {
			bucketName, _, isObjectPath := parseObjectPath(r.URL.Path)
			if !isObjectPath {
				bucketName = bucketOnlyName(r.URL.Path)
			}
			if bucketName != "" && isReadOnlyMethod(r.Method) && isPublicBucket(bucketName) {
				publicHandler.ServeHTTP(w, r)
				return
			}
			if bucketName == "" {
				// ListBuckets without credentials: denying it keeps the bucket
				// inventory of a multi-tenant gateway private.
				writeAccessDenied(w, r, "", "Anonymous access is not allowed")
				return
			}
			writeAccessDenied(w, r, bucketName, "Anonymous access to bucket "+bucketName+" is not allowed")
			return
		}

		var matchedRule *S3BucketAuthRule
		for i := range rules {
			if rules[i].AccessKey == ak {
				matchedRule = &rules[i]
				break
			}
		}

		if matchedRule == nil {
			// Never hand an unknown key to gofakes3: with no keys registered it
			// accepts any signature, which would turn a stale client key into
			// anonymous access to every bucket.
			log.Warnf("S3 access denied: unknown access key=%s", ak)
			writeInvalidAccessKey(w, r)
			return
		}

		bucketName, _, isObjectPath := parseObjectPath(r.URL.Path)
		if !isObjectPath {
			bucketName = bucketOnlyName(r.URL.Path)
		}
		// Root request (e.g. ListBuckets: GET /)
		if bucketName == "" {
			ctx := context.WithValue(r.Context(), s3AuthRuleContextKey, matchedRule)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Bucket permission check
		if !matchedRule.MatchesBucket(bucketName) {
			log.Warnf("S3 access denied: key=%s has no permission on bucket=%s", ak, bucketName)
			writeAccessDenied(w, r, bucketName, "Access Denied to bucket "+bucketName)
			return
		}

		// Write permission check. The bucket is known here, so use the per-bucket
		// view: one access key can be read-only on one bucket and writable on
		// another.
		if !matchedRule.CanWriteBucket(bucketName) {
			switch r.Method {
			case http.MethodPut, http.MethodPost, http.MethodDelete:
				log.Warnf("S3 write denied: key=%s is readonly on bucket=%s", ak, bucketName)
				writeAccessDenied(w, r, bucketName, "Write operations not permitted for this access key")
				return
			}
		}

		// gofakes3 resolves X-Amz-Copy-Source itself and authorizes nothing, so
		// a key scoped to one bucket could read any other bucket by copying out
		// of it. The source is therefore checked against the same rule as the
		// destination.
		if srcBucket, srcKey, present := copySourceRef(r); present {
			if !copySourceIsSigned(r) {
				// A presigned URL signs the headers it lists in
				// X-Amz-SignedHeaders. Anyone holding an upload URL could append
				// X-Amz-Copy-Source and turn the upload into a server-side copy of
				// an object inside the same bucket; AWS rejects that because every
				// x-amz-* header has to be signed.
				writeS3Error(w, r, http.StatusForbidden, "AccessDenied", bucketName,
					"X-Amz-Copy-Source must be covered by the signature (list it in X-Amz-SignedHeaders).")
				return
			}
			if srcBucket == "" || cleanObjectKey(srcKey) != nil {
				writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument", bucketName,
					"X-Amz-Copy-Source must name a bucket and a key")
				return
			}
			if !matchedRule.MatchesBucket(srcBucket) {
				log.Warnf("S3 access denied: key=%s may not read bucket=%s (copy source)", ak, srcBucket)
				writeAccessDenied(w, r, srcBucket, "Access Denied to bucket "+srcBucket)
				return
			}
		}

		// Authorized, inject matched rule into context and continue
		ctx := context.WithValue(r.Context(), s3AuthRuleContextKey, matchedRule)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// publicBucketGuard is a last line of defence in front of the keyless gofakes3
// instance: it re-checks that whatever reaches it is an anonymous read of a
// bucket that is still flagged public.
func publicBucketGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bucketName, _, isObjectPath := parseObjectPath(r.URL.Path)
		if !isObjectPath {
			bucketName = bucketOnlyName(r.URL.Path)
		}
		if bucketName == "" || !isReadOnlyMethod(r.Method) || !isPublicBucket(bucketName) {
			writeAccessDenied(w, r, bucketName, "Anonymous access to bucket "+bucketName+" is not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// s3BucketAuthorized checks if the request is permitted to access the bucket according to rules.
func s3BucketAuthorized(r *http.Request, bucket string) bool {
	rules, misconfigured := loadS3BucketAuthRules()
	if misconfigured {
		// These helpers run in front of handlers that may answer without a
		// second look (the direct-upload redirect, the CORS endpoint), so a
		// broken setting has to deny here as well.
		return false
	}
	if len(rules) == 0 {
		return true
	}
	ak := extractAccessKey(r)
	if ak == "" {
		return false
	}
	for i := range rules {
		if rules[i].AccessKey == ak {
			return rules[i].MatchesBucket(bucket)
		}
	}
	return false
}

// s3BucketReadAllowed reports whether the request may read (GET/HEAD) the
// bucket either through its credentials or because the bucket is public. Only
// read paths may call this; writes must use s3BucketCanWrite.
func s3BucketReadAllowed(r *http.Request, bucket string) bool {
	if s3BucketAuthorized(r, bucket) {
		return true
	}
	return extractAccessKey(r) == "" && isPublicBucket(bucket)
}

// s3BucketCanWrite checks if the request has write permission on any bucket of
// the matching rule. Callers that know the bucket should use
// s3BucketCanWriteTo, which respects per-bucket read-only settings.
func s3BucketCanWrite(r *http.Request) bool {
	bucket := bucketOnlyName(r.URL.Path)
	if name, _, isObjectPath := parseObjectPath(r.URL.Path); isObjectPath {
		bucket = name
	}
	return s3BucketCanWriteTo(r, bucket)
}

// s3BucketCanWriteTo reports whether the request may write this bucket.
func s3BucketCanWriteTo(r *http.Request, bucket string) bool {
	rules, misconfigured := loadS3BucketAuthRules()
	if misconfigured {
		return false
	}
	if len(rules) == 0 {
		return true
	}
	ak := extractAccessKey(r)
	if ak == "" {
		return false
	}
	for i := range rules {
		if rules[i].AccessKey == ak {
			return rules[i].CanWriteBucket(bucket)
		}
	}
	return false
}
