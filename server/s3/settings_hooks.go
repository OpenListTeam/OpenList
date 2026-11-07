package s3

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// The S3 settings were only ever validated on the S3 protocol side: the admin
// save path accepted any string, returned success, and left a gateway that
// answered 503 to every request (bucket credentials), served no bucket (bucket
// list) or silently dropped CORS policies. These hooks move the check to the
// save, so a bad value is refused while the old one keeps working.
const (
	maxS3AuthSettingBytes     = 256 << 10
	maxS3BucketsSettingBytes  = 256 << 10
	maxS3CORSConfigBytes      = 512 << 10
	maxS3BucketAuthRuleCount  = 1000
	maxS3BucketEntriesPerList = 1000
)

func init() {
	op.RegisterSettingItemHook(conf.S3BucketAuth, func(item *model.SettingItem) error {
		return checkS3BucketAuthSetting(item.Value)
	})
	op.RegisterSettingItemHook(conf.S3Buckets, func(item *model.SettingItem) error {
		return checkS3BucketsSetting(item.Value)
	})
	op.RegisterSettingItemHook(conf.S3BucketCors, func(item *model.SettingItem) error {
		return checkS3CORSConfigSetting(item.Value)
	})
	op.RegisterSettingItemHook(conf.S3Mode, func(item *model.SettingItem) error {
		raw := strings.ToLower(strings.TrimSpace(item.Value))
		if raw != "" && raw != S3ModePerformance && raw != S3ModeCompat {
			return fmt.Errorf("%s must be %q or %q, got %q", conf.S3Mode, S3ModePerformance, S3ModeCompat, item.Value)
		}
		return nil
	})
}

// checkS3BucketAuthSetting validates the bucket credential document.
func checkS3BucketAuthSetting(value string) error {
	raw := strings.TrimSpace(value)
	if raw == "" || raw == "[]" {
		return nil
	}
	if len(raw) > maxS3AuthSettingBytes {
		return fmt.Errorf("%s is too large (%d bytes, at most %d)", conf.S3BucketAuth, len(raw), maxS3AuthSettingBytes)
	}
	// The error must not quote the value: it holds a secret per rule and this
	// message travels back to the admin UI and into logs.
	var rules []S3BucketAuthRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return fmt.Errorf("%s must be a JSON array of rules: %v", conf.S3BucketAuth, err)
	}
	if len(rules) > maxS3BucketAuthRuleCount {
		return fmt.Errorf("%s may not contain more than %d rules", conf.S3BucketAuth, maxS3BucketAuthRuleCount)
	}
	for i, rule := range rules {
		key := strings.TrimSpace(rule.AccessKey)
		if key == "" {
			return fmt.Errorf("%s rule %d has no access_key_id", conf.S3BucketAuth, i)
		}
		// gofakes3 cannot parse a credential whose access key is shorter than
		// three characters, so the rule is dropped at load time and every
		// request signed with it answers 403 - a value the save accepted that
		// could never work. Refuse it while the admin is still looking at it.
		if len(key) < minS3AccessKeyLength {
			return fmt.Errorf("%s rule %d: an access_key_id needs at least %d characters", conf.S3BucketAuth, i, minS3AccessKeyLength)
		}
		if strings.TrimSpace(rule.SecretKey) == "" {
			return fmt.Errorf("%s rule %d has no secret_access_key", conf.S3BucketAuth, i)
		}
		if len(rule.Buckets) > maxS3BucketEntriesPerList {
			return fmt.Errorf("%s rule %d lists more than %d buckets", conf.S3BucketAuth, i, maxS3BucketEntriesPerList)
		}
	}
	return nil
}

// checkS3BucketsSetting validates the bucket list, including the paths: a bucket
// whose path resolves to the virtual root (empty, "/" or a ".." that cleans away)
// owns every storage mount, and a public bucket in that shape serves all of them
// anonymously.
func checkS3BucketsSetting(value string) error {
	raw := strings.TrimSpace(value)
	if raw == "" || raw == "[]" {
		return nil
	}
	if len(raw) > maxS3BucketsSettingBytes {
		return fmt.Errorf("%s is too large (%d bytes, at most %d)", conf.S3Buckets, len(raw), maxS3BucketsSettingBytes)
	}
	var buckets []Bucket
	if err := json.Unmarshal([]byte(raw), &buckets); err != nil {
		return fmt.Errorf("%s must be a JSON array of buckets: %v", conf.S3Buckets, err)
	}
	seen := make(map[string]bool, len(buckets))
	paths := make(map[string]string, len(buckets))
	for i, bucket := range buckets {
		name := strings.TrimSpace(bucket.Name)
		if name == "" {
			return fmt.Errorf("%s bucket %d has no name", conf.S3Buckets, i)
		}
		// A name containing "*" is not a name: s3_bucket_auth matches "*" and
		// "prefix*" as wildcards, so a bucket called "*" makes every per-bucket
		// key cover every bucket. Whitespace and slashes cannot appear in a
		// request path either, so they can never address a bucket.
		if strings.Contains(name, "*") {
			return fmt.Errorf("%s bucket %q: a bucket name cannot contain \"*\" (the auth rules would treat it as a wildcard)", conf.S3Buckets, name)
		}
		if strings.ContainsAny(name, "/\\ \t\r\n") {
			return fmt.Errorf("%s bucket %q: a bucket name cannot contain slashes or whitespace", conf.S3Buckets, name)
		}
		if seen[name] {
			return fmt.Errorf("%s defines bucket %q twice", conf.S3Buckets, name)
		}
		seen[name] = true
		if bucket.Mode != "" {
			mode := strings.ToLower(strings.TrimSpace(bucket.Mode))
			if mode != S3ModePerformance && mode != S3ModeCompat {
				return fmt.Errorf("%s bucket %q has invalid mode %q: must be %q or %q",
					conf.S3Buckets, name, bucket.Mode, S3ModePerformance, S3ModeCompat)
			}
		}
		cleaned, err := cleanBucketPath(name, bucket.Path)
		if err != nil {
			return fmt.Errorf("%s bucket %q cannot be used: %v", conf.S3Buckets, name, err)
		}
		// Two buckets on one path share a backing directory, so the read-only
		// one can be written through the writable one.
		if owner, ok := paths[cleaned]; ok {
			return fmt.Errorf("%s buckets %q and %q use the same path %q", conf.S3Buckets, owner, name, cleaned)
		}
		paths[cleaned] = name
	}
	return nil
}

// checkS3CORSConfigSetting validates the per-bucket CORS document. It applies the
// document's shape limits only: the rule schema itself is enforced when a bucket
// writes through the S3 API.
func checkS3CORSConfigSetting(value string) error {
	raw := strings.TrimSpace(value)
	if raw == "" || raw == "{}" {
		return nil
	}
	if len(raw) > maxS3CORSConfigBytes {
		return fmt.Errorf("%s is too large (%d bytes, at most %d)", conf.S3BucketCors, len(raw), maxS3CORSConfigBytes)
	}
	var config map[string][]s3CORSRule
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return fmt.Errorf("%s must be a JSON object keyed by bucket: %v", conf.S3BucketCors, err)
	}
	for bucket, rules := range config {
		if len(rules) > s3CORSMaxRules {
			return fmt.Errorf("%s bucket %q has more than %d rules", conf.S3BucketCors, bucket, s3CORSMaxRules)
		}
		for _, rule := range rules {
			for _, list := range [][]string{rule.AllowedOrigins, rule.AllowedMethods, rule.AllowedHeaders, rule.ExposeHeaders} {
				if err := checkS3CORSListBounds(list); err != nil {
					return fmt.Errorf("%s bucket %q: %v", conf.S3BucketCors, bucket, err)
				}
			}
		}
	}
	return nil
}
