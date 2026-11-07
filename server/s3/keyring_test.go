package s3

import (
	"encoding/json"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/stretchr/testify/assert"
)

// recordingSink remembers what the ring asked gofakes3 to add/remove.
type recordingSink struct {
	added   []map[string]string
	removed [][]string
	// calls keeps the order of the calls, which matters: gofakes3 accepts
	// everything while its key map is empty, so a replacement must add first.
	calls []string
}

func (s *recordingSink) AddAuthKeys(p map[string]string) {
	cp := make(map[string]string, len(p))
	for k, v := range p {
		cp[k] = v
	}
	s.added = append(s.added, cp)
	s.calls = append(s.calls, "add")
}

func (s *recordingSink) DelAuthKeys(p []string) {
	s.removed = append(s.removed, append([]string(nil), p...))
	s.calls = append(s.calls, "del")
}

func setBucketAuthRules(t *testing.T, rules []S3BucketAuthRule) {
	t.Helper()
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	if rules == nil {
		setTestSetting(conf.S3BucketAuth, "")
		return
	}
	raw, err := json.Marshal(rules)
	assert.NoError(t, err)
	setTestSetting(conf.S3BucketAuth, string(raw))
}

func TestKeyRing_SyncAppliesDiffs(t *testing.T) {
	t.Cleanup(func() { setBucketAuthRules(t, nil) })

	setBucketAuthRules(t, []S3BucketAuthRule{
		{AccessKey: "app-a", SecretKey: "secret-a", Buckets: []string{"a"}},
	})
	ring := newKeyRing(resolveAuthPairs())
	sink := &recordingSink{}
	ring.bind(sink)

	// First sync with nothing changed must stay silent: the ring was seeded
	// from the same settings, so gofakes3 already has these keys.
	assert.False(t, ring.sync())
	assert.Empty(t, sink.added)
	assert.Empty(t, sink.removed)

	// Rotate a secret and add a key, the way an admin save would.
	setBucketAuthRules(t, []S3BucketAuthRule{
		{AccessKey: "app-a", SecretKey: "secret-a2", Buckets: []string{"a"}},
		{AccessKey: "app-b", SecretKey: "secret-b", Buckets: []string{"b"}},
	})
	assert.True(t, ring.sync())
	assert.Equal(t, map[string]string{"app-a": "secret-a2", "app-b": "secret-b"}, sink.added[0])
	assert.Empty(t, sink.removed)

	// A key removed from the settings must be dropped from the instance.
	setBucketAuthRules(t, []S3BucketAuthRule{
		{AccessKey: "app-b", SecretKey: "secret-b", Buckets: []string{"b"}},
	})
	assert.True(t, ring.sync())
	assert.Equal(t, []string{"app-a"}, sink.removed[0])

	secret, ok := ring.secret("app-b")
	assert.True(t, ok)
	assert.Equal(t, "secret-b", secret)
	assert.False(t, ring.empty())
}

func TestKeyRing_LegacyGlobalKey(t *testing.T) {
	t.Cleanup(func() { setBucketAuthRules(t, nil) })

	setTestSetting(conf.S3BucketAuth, "")
	setTestSetting(conf.S3AccessKeyId, "legacy")
	setTestSetting(conf.S3SecretAccessKey, "legacy-secret")

	ring := newKeyRing(resolveAuthPairs())
	secret, ok := ring.secret("legacy")
	assert.True(t, ok)
	assert.Equal(t, "legacy-secret", secret)

	// Clearing the global key empties the ring, which puts the gateway back in
	// upstream's open mode instead of leaving a dead credential behind.
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	assert.True(t, ring.sync())
	assert.True(t, ring.empty())
}

// A tick where nothing changed must not even parse the rules: that is what
// keeps a 2s watch loop at string-comparison cost.
func TestKeyRing_SyncSkipsWorkWhenUnchanged(t *testing.T) {
	t.Cleanup(func() { setBucketAuthRules(t, nil) })

	setBucketAuthRules(t, []S3BucketAuthRule{
		{AccessKey: "app-a", SecretKey: "secret-a", Buckets: []string{"a"}},
	})
	ring := newKeyRing(resolveAuthPairs())
	calls := 0
	ring.lookup = func() map[string]string {
		calls++
		return resolveAuthPairsQuiet()
	}
	assert.False(t, ring.sync())
	assert.Equal(t, 0, calls, "unchanged settings must not rebuild the key map")

	setBucketAuthRules(t, []S3BucketAuthRule{
		{AccessKey: "app-a", SecretKey: "secret-a2", Buckets: []string{"a"}},
	})
	assert.True(t, ring.sync())
	assert.Equal(t, 1, calls)
}

// A malformed rule list must not spam the log on every request.
func TestWarnBadAuthRulesOnce(t *testing.T) {
	setTestSetting(conf.S3BucketAuth, "")
	setTestSetting(conf.S3AccessKeyId, "")
	badAuthRulesSeen = ""
	assert.True(t, warnBadAuthRulesOnce("{oops"))
	assert.False(t, warnBadAuthRulesOnce("{oops"))
	assert.True(t, warnBadAuthRulesOnce("{oops2"))
}
