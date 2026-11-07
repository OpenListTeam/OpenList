// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// authKeySink is the slice of gofakes3.GoFakeS3 the ring needs: the gateway
// keeps its own copy of the accepted keys and only exposes Add/Del on them.
type authKeySink interface {
	AddAuthKeys(map[string]string)
	DelAuthKeys([]string)
}

// keyRing keeps the credentials the gateway accepts in sync with the settings
// table while the process runs.
//
// gofakes3 is constructed once, at boot, with a snapshot of
// accessKey->secretKey; signature verification reads that snapshot, so a key
// added (or a secret rotated) through the admin UI stayed invisible until the
// process restarted. The ring re-reads the settings and applies only the diff
// through AddAuthKeys/DelAuthKeys, so a save is picked up within one tick.
// Bucket scope, readonly and the public flag are already resolved per request
// and never went stale.
type keyRing struct {
	mu     sync.RWMutex
	keys   map[string]string
	sinks  []authKeySink
	lookup func() map[string]string
	// raw is the settings fingerprint the current keys were built from. The
	// tick only re-parses the JSON when the raw strings actually changed, so
	// the steady-state cost of watching is three string comparisons.
	raw authFingerprint
}

func newKeyRing(seed map[string]string) *keyRing {
	if seed == nil {
		seed = map[string]string{}
	}
	cp := make(map[string]string, len(seed))
	for k, v := range seed {
		cp[k] = v
	}
	// The seed was just read from the settings, so record the matching
	// fingerprint; otherwise the very first tick would re-apply a no-op diff.
	return &keyRing{keys: cp, lookup: resolveAuthPairsQuiet, raw: authSettingsFingerprint()}
}

// snapshot returns a copy of the keys currently accepted, for gofakes3's boot
// time WithV4Auth option.
func (k *keyRing) snapshot() map[string]string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make(map[string]string, len(k.keys))
	for a, s := range k.keys {
		out[a] = s
	}
	return out
}

func (k *keyRing) bind(sinks ...authKeySink) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sinks = append(k.sinks, sinks...)
}

// empty reports whether the gateway currently accepts no key at all, which is
// upstream's "no credentials configured" open mode.
func (k *keyRing) empty() bool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return len(k.keys) == 0
}

// secret implements the gofakes3 lookup signature used by the redirect path.
func (k *keyRing) secret(accessKey string) (string, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	s, ok := k.keys[accessKey]
	return s, ok
}

// sync pulls the current settings into the ring. It reports whether anything
// changed; the caller decides whether that is worth a log line.
func (k *keyRing) sync() bool {
	raw := authSettingsFingerprint()

	k.mu.RLock()
	unchanged := raw == k.raw
	k.mu.RUnlock()
	if unchanged {
		return false
	}

	want := k.lookup()
	if want == nil {
		want = map[string]string{}
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	var del []string
	add := make(map[string]string)
	for a, s := range want {
		if cur, ok := k.keys[a]; !ok || cur != s {
			add[a] = s
		}
	}
	for a := range k.keys {
		if _, ok := want[a]; !ok {
			del = append(del, a)
		}
	}
	if len(add) == 0 && len(del) == 0 {
		// Nothing to apply, but remember the fingerprint: that is what keeps
		// the next ticks to a string comparison.
		k.raw = raw
		return false
	}

	for _, sink := range k.sinks {
		// Del before Add: a rotated secret must not be deleted after the new
		// one landed, or the key would silently stop working.
		if len(del) > 0 {
			sink.DelAuthKeys(del)
		}
		if len(add) > 0 {
			sink.AddAuthKeys(add)
		}
	}
	k.keys = want
	k.raw = raw
	return true
}

// authFingerprint is what the accepted keys are derived from: the rule list
// plus the legacy single key. Comparing it is far cheaper than parsing the JSON
// on every tick, and a fixed-size struct keeps the comparison allocation free.
type authFingerprint [3]string

func authSettingsFingerprint() authFingerprint {
	return authFingerprint{
		setting.GetStr(conf.S3BucketAuth),
		setting.GetStr(conf.S3AccessKeyId),
		setting.GetStr(conf.S3SecretAccessKey),
	}
}

// watch applies settings changes until ctx is cancelled. The interval is a
// deliberate compromise: settings are an in-memory map, so reading them is
// cheap, but the diff is only interesting after an admin save.
func (k *keyRing) watch(ctx context.Context) {
	const interval = 2 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if k.sync() {
				utils.Log.Infof("S3 credentials reloaded, %d key(s) active", len(k.snapshot()))
			}
		}
	}
}
