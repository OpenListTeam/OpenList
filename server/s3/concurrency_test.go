package s3

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/gofakes3"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A configuration that becomes unusable must not empty the ring: gofakes3
// verifies no signature at all while its key map is empty, so applying "no
// usable key" over a working set silently opens the gateway. The ring has to
// keep what works and retry, not latch onto the broken state.
func TestKeyRingKeepsKeysWhenConfigurationBecomesUnusable(t *testing.T) {
	t.Cleanup(func() { setBucketAuthRules(t, nil) })

	// Deterministic starting point: the ring's fingerprint must be the
	// configuration it was seeded from.
	setTestSetting(conf.S3BucketAuth, "")
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	ring := newKeyRing(map[string]string{"good-key": "good-secret"})

	// Configured, but every rule is rejected: a rule without an access key can
	// never authenticate anything.
	setTestSetting(conf.S3BucketAuth, `[{"access_key_id":"","secret_access_key":"x","buckets":["*"]}]`)
	badAuthRulesSeen = ""

	assert.False(t, ring.sync(), "nothing could be applied, so nothing changed")
	assert.False(t, ring.empty(), "an unusable configuration must not clear the working keys")

	// The ring must not have recorded the broken configuration as applied:
	// recovering has to happen without a restart.
	setBucketAuthRules(t, []S3BucketAuthRule{
		{AccessKey: "good-key", SecretKey: "good-secret", Buckets: []string{"*"}},
		{AccessKey: "second-key", SecretKey: "second-secret", Buckets: []string{"mp"}},
	})
	assert.True(t, ring.sync())
	assert.Len(t, ring.snapshot(), 2)
}

// Deleting every credential is a deliberate act and must still reach the
// documented open mode.
func TestKeyRingClearsKeysWhenCredentialsAreRemoved(t *testing.T) {
	t.Cleanup(func() { setBucketAuthRules(t, nil) })

	setTestSetting(conf.S3BucketAuth, "")
	setTestSetting(conf.S3AccessKeyId, "old-key")
	setTestSetting(conf.S3SecretAccessKey, "old-secret")
	ring := newKeyRing(map[string]string{"old-key": "old-secret"})

	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")

	assert.True(t, ring.sync())
	assert.True(t, ring.empty())
}

// A PUT writes the content first and the metadata second, with no natural
// barrier between them. Two PUTs to the same key must not be inside that pair
// at the same time, or the object can end up with the bytes of one request
// under the headers of the other.
func TestConcurrentPutKeepsContentAndMetadataTogether(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	var inside, peak int32
	b.putPausedAfterContent = func() {
		n := atomic.AddInt32(&inside, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if n <= old || atomic.CompareAndSwapInt32(&peak, old, n) {
				break
			}
		}
		// Hold the window open long enough that an unprotected second write
		// would certainly step into it.
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&inside, -1)
	}

	type write struct {
		body  []byte
		ctype string
	}
	a := write{body: bytes.Repeat([]byte("a"), 111), ctype: "text/x-aaa"}
	bb := write{body: bytes.Repeat([]byte("b"), 333), ctype: "text/x-bbb"}

	for round := 0; round < 10; round++ {
		key := fmt.Sprintf("race/put-%d.bin", round)
		var wg sync.WaitGroup
		for _, w := range []write{a, bb} {
			wg.Add(1)
			go func(w write) {
				defer wg.Done()
				_, err := b.PutObject(ctx, "mp", key, map[string]string{"Content-Type": w.ctype},
					bytes.NewReader(w.body), int64(len(w.body)))
				assert.NoError(t, err)
			}(w)
		}
		wg.Wait()

		obj, err := b.HeadObject(ctx, "mp", key)
		require.NoError(t, err)
		size, ctype := obj.Size, obj.Metadata["Content-Type"]
		switch {
		case size == int64(len(a.body)) && ctype == a.ctype:
		case size == int64(len(bb.body)) && ctype == bb.ctype:
		default:
			t.Fatalf("round %d: object is torn: size=%d content-type=%q (want either %d/%q or %d/%q)",
				round, size, ctype, len(a.body), a.ctype, len(bb.body), bb.ctype)
		}
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&peak),
		"two PUTs of the same key were between content and metadata at the same time")
}

// A concurrent PUT and DELETE of one key are two legal operations; one of them
// wins and the other reports the harmless outcome, neither returns a 500.
func TestConcurrentPutAndDeleteDoNotFail(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	for round := 0; round < 40; round++ {
		key := fmt.Sprintf("race/del-%d.bin", round)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := b.PutObject(ctx, "mp", key, nil, strings.NewReader("payload"), 7)
			assert.NoError(t, err)
		}()
		go func() {
			defer wg.Done()
			_, err := b.DeleteObject(ctx, "mp", key)
			assert.NoError(t, err)
		}()
		wg.Wait()
	}
}

// Creating uploads is cheap and nothing reclaims them before the TTL, so the
// number in flight has to be bounded.
func TestMultipartUploadsAreBounded(t *testing.T) {
	previous := maxMultipartUploads
	maxMultipartUploads = 3
	t.Cleanup(func() { maxMultipartUploads = previous })

	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	var created []gofakes3.UploadID
	for i := int64(0); i < maxMultipartUploads; i++ {
		id, err := b.CreateMultipartUpload(ctx, "mp", fmt.Sprintf("bound/%d.bin", i), nil)
		require.NoError(t, err)
		created = append(created, id)
	}
	_, err := b.CreateMultipartUpload(ctx, "mp", "bound/over.bin", nil)
	require.Error(t, err, "the upload above the limit must be refused")
	assert.Contains(t, err.Error(), "SlowDown")

	// Aborting releases the slot again.
	require.NoError(t, b.AbortMultipartUpload(ctx, "mp", "bound/0.bin", created[0]))
	_, err = b.CreateMultipartUpload(ctx, "mp", "bound/after-abort.bin", nil)
	assert.NoError(t, err)
}

// A delete that lands while an access batch is in flight must not be undone by
// the batch's upsert: the row it writes belongs to an object that is gone.
func TestAccessFlushDoesNotResurrectDeletedObject(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	const key = "/mp/gone-during-flush.bin"

	b.recordAccess(key)

	// The delete happens between collecting the batch and writing it.
	b.flushTestHook = func() {
		b.flushTestHook = nil
		b.access.forget(key)
	}
	b.flushAccess()

	var count int64
	require.NoError(t, db.GetDb().Model(&model.S3ObjectMetadata{}).Where("path = ?", key).Count(&count).Error)
	assert.Zero(t, count, "the access row of a deleted object was written back by an in-flight batch")
}
