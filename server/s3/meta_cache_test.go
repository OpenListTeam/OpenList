package s3

import (
	"fmt"
	"sync"
	"testing"
)

// TestMetaCacheIsBounded pins that the per-object metadata cache does not grow
// with the number of objects this process has written. A miss only costs a
// database lookup, so spilling the cache is always safe — growing forever is not.
func TestMetaCacheIsBounded(t *testing.T) {
	b := &s3Backend{meta: new(sync.Map), uploads: new(sync.Map)}

	for i := 0; i < metaCacheLimit*2; i++ {
		b.cacheObjectMetadata(fmt.Sprintf("/bucket/obj-%d.txt", i), objectMetadata{
			headers: map[string]string{"x-amz-meta-n": fmt.Sprint(i)},
		})
	}

	count := 0
	b.meta.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count > metaCacheLimit {
		t.Fatalf("cache holds %d entries, want at most %d", count, metaCacheLimit)
	}
	if b.metaSize > metaCacheLimit {
		t.Fatalf("counted size = %d, want at most %d", b.metaSize, metaCacheLimit)
	}
	// A cache that stays usable after spilling: the last write is readable.
	b.cacheObjectMetadata("/bucket/last.txt", objectMetadata{headers: map[string]string{"x-amz-meta-k": "v"}})
	if _, ok := b.meta.Load("/bucket/last.txt"); !ok {
		t.Fatal("cache did not accept a write after spilling")
	}
}
