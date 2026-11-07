package s3

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
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
}

// UnmarshalJSON accepts both the canonical access_key_id / secret_access_key
// spelling and the older access_key / secret_key one. The setting is plain
// JSON edited by hand as often as by the admin UI, so a silently ignored key
// name would leave the rule without credentials.
func (r *S3BucketAuthRule) UnmarshalJSON(data []byte) error {
	var raw struct {
		AccessKeyID     string   `json:"access_key_id"`
		SecretAccessKey string   `json:"secret_access_key"`
		AccessKey       string   `json:"access_key"`
		SecretKey       string   `json:"secret_key"`
		Buckets         []string `json:"buckets"`
		ReadOnly        bool     `json:"readonly"`
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
	r.ReadOnly = raw.ReadOnly
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
func (r *S3BucketAuthRule) CanWrite() bool {
	if r == nil {
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
func warnBadAuthRulesOnce(raw string) bool {
	badAuthRulesMu.Lock()
	defer badAuthRulesMu.Unlock()
	if badAuthRulesSeen == raw {
		return false
	}
	badAuthRulesSeen = raw
	return true
}

func getS3BucketAuthRules() []S3BucketAuthRule {
	raw := strings.TrimSpace(setting.GetStr(conf.S3BucketAuth))
	var parsed []S3BucketAuthRule
	if raw != "" && raw != "[]" {
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			// This runs on every request; warn once per distinct bad value
			// instead of flooding the log while an admin fixes the JSON.
			if warnBadAuthRulesOnce(raw) {
				log.Warnf("Failed to parse %s as JSON: %v", conf.S3BucketAuth, raw)
			}
			parsed = nil
		}
	}

	// A rule with no access key can never authenticate a request. Keeping it
	// around would only flip the gateway into "credentials are configured"
	// mode and lock out every anonymous client, so such rules are dropped.
	//
	// Rules that repeat an access key are merged into one (union of buckets,
	// readonly only when every duplicate is readonly). Every lookup below stops
	// at the first rule carrying the key, so a second rule for the same key
	// would shadow the rest: the admin UI used to write one row per bucket even
	// when every row carried the master key, and in that shape the key could
	// reach only the first bucket listed and got 403 on all the others.
	rules := make([]S3BucketAuthRule, 0, len(parsed)+1)
	byKey := make(map[string]int, len(parsed)+1)
	for _, rule := range parsed {
		key := strings.TrimSpace(rule.AccessKey)
		if key == "" {
			continue
		}
		rule.AccessKey = key
		if at, ok := byKey[key]; ok {
			rules[at].Buckets = mergeBucketLists(rules[at].Buckets, rule.Buckets)
			rules[at].ReadOnly = rules[at].ReadOnly && rule.ReadOnly
			if rules[at].SecretKey == "" {
				rules[at].SecretKey = rule.SecretKey
			}
			continue
		}
		byKey[key] = len(rules)
		rules = append(rules, rule)
	}

	// The legacy global key keeps working after per-bucket rules are added, and
	// it always means "every bucket, read/write": that is the contract the
	// admin UI states, and it is also the safety net for a per-bucket row that
	// was saved with the master key by mistake. It is kept first so a
	// per-bucket rule reusing that key cannot shadow it.
	ak := strings.TrimSpace(setting.GetStr(conf.S3AccessKeyId))
	sk := strings.TrimSpace(setting.GetStr(conf.S3SecretAccessKey))
	if ak != "" {
		global := S3BucketAuthRule{
			AccessKey: ak,
			SecretKey: sk,
			Buckets:   []string{"*"},
		}
		if at, ok := byKey[ak]; ok {
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
	return rules
}

// mergeBucketLists unions two bucket lists, keeping first-seen order. A "*" on
// either side wins outright: it already covers everything else.
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
	rules := getS3BucketAuthRules()
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

// extractAccessKey extracts the AccessKeyId from either AWS V4 / V2 Authorization header or Query params.
func extractAccessKey(r *http.Request) string {
	// 1. Check Query parameter: X-Amz-Credential (V4 presigned URL)
	if cred := r.URL.Query().Get("X-Amz-Credential"); cred != "" {
		if ak := v4CredentialAccessKey(cred); ak != "" {
			return ak
		}
	}

	// 2. Check Query parameter: AWSAccessKeyId (V2 presigned URL)
	if ak := r.URL.Query().Get("AWSAccessKeyId"); ak != "" {
		return ak
	}

	// 3. Check Authorization header
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}

	// AWS V4: "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=..."
	if idx := strings.Index(auth, "Credential="); idx != -1 {
		credStr := auth[idx+len("Credential="):]
		if commaIdx := strings.Index(credStr, ","); commaIdx != -1 {
			credStr = credStr[:commaIdx]
		}
		if ak := v4CredentialAccessKey(credStr); ak != "" {
			return ak
		}
	}

	// AWS V2: "AWS AKIAIOSFODNN7EXAMPLE:signature"
	if strings.HasPrefix(auth, "AWS ") {
		token := strings.TrimPrefix(auth, "AWS ")
		parts := strings.Split(token, ":")
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
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
		rules := getS3BucketAuthRules()
		if len(rules) == 0 {
			// No key configured at all: keep upstream's anonymous, fully open
			// behaviour for deployments that never set up S3 credentials.
			next.ServeHTTP(w, r)
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

		// Write permission check
		if !matchedRule.CanWrite() {
			switch r.Method {
			case http.MethodPut, http.MethodPost, http.MethodDelete:
				log.Warnf("S3 write denied: key=%s is readonly on bucket=%s", ak, bucketName)
				writeAccessDenied(w, r, bucketName, "Write operations not permitted for this access key")
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
	rules := getS3BucketAuthRules()
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

// s3BucketCanWrite checks if the request has write permission.
func s3BucketCanWrite(r *http.Request) bool {
	rules := getS3BucketAuthRules()
	if len(rules) == 0 {
		return true
	}
	ak := extractAccessKey(r)
	if ak == "" {
		return false
	}
	for i := range rules {
		if rules[i].AccessKey == ak {
			return rules[i].CanWrite()
		}
	}
	return false
}
