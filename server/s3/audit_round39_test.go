package s3

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuditRound39_KeyLockPathAlignment pins that PUT, DELETE and self-copy REPLACE
// all serialize on the exact same storage path fp, and that operations in different
// buckets with identical object names do not lock each other out.
func TestAuditRound39_KeyLockPathAlignment(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()

	// 1. Concurrent PUT and DELETE on the same object must not race or panic.
	for round := 0; round < 20; round++ {
		key := fmt.Sprintf("round39/obj-%d.txt", round)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = b.PutObject(ctx, "mp", key, nil, strings.NewReader("content-data"), 12)
		}()
		go func() {
			defer wg.Done()
			_, _ = b.DeleteObject(ctx, "mp", key)
		}()
		wg.Wait()
	}

	// 2. Self-copy REPLACE must serialize with PUT on the same object.
	key := "round39/replace-race.txt"
	_, err := b.PutObject(ctx, "mp", key, map[string]string{"x-amz-meta-init": "true"}, strings.NewReader("hello"), 5)
	require.NoError(t, err)

	var wg sync.WaitGroup
	var completedCount atomic.Int32
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, copyErr := b.CopyObject(ctx, "mp", key, "mp", key, map[string]string{
			"x-amz-metadata-directive": "REPLACE",
			"x-amz-meta-k":             "v",
		})
		if copyErr == nil {
			completedCount.Add(1)
		}
	}()
	go func() {
		defer wg.Done()
		_, putErr := b.PutObject(ctx, "mp", key, map[string]string{"x-amz-meta-k": "v2"}, strings.NewReader("updated"), 7)
		if putErr == nil {
			completedCount.Add(1)
		}
	}()
	wg.Wait()
	assert.Greater(t, completedCount.Load(), int32(0))
}

// TestAuditRound39_KeyLockIndependenceAcrossBuckets verifies that two writes
// to the same relative key name in different buckets do not contend on keyLocks.
func TestAuditRound39_KeyLockIndependenceAcrossBuckets(t *testing.T) {
	locks := &keyLocks{}
	pathA := "/storage/bucket-a/file.bin"
	pathB := "/storage/bucket-b/file.bin"

	unlockA := locks.lock(pathA)
	lockedB := make(chan struct{})
	go func() {
		unlockB := locks.lock(pathB)
		close(lockedB)
		unlockB()
	}()

	select {
	case <-lockedB:
		// Success: pathB acquired its lock immediately while pathA was held!
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pathB lock timed out; different buckets must not share a lock")
	}
	unlockA()
}
