package s3

import "sync"

// keyLocks serialises the mutating operations on one object.
//
// A PUT is two steps - the content goes to the driver, then the metadata
// (content type, x-amz-meta-*, checksums) goes to its own store - and neither
// step knows about the other. Two concurrent PUTs to the same key could
// therefore interleave, leaving the bytes of one request under the headers of
// the other: a HEAD then reports a size from the content and a Content-Type
// from a different write. S3 promises that a PUT replaces an object atomically,
// so the pair has to be made indivisible. The same lock covers DELETE, which
// otherwise raced the write it was deleting and turned a legal pair of
// operations into a 500.
type keyLocks struct {
	mu    sync.Mutex
	locks map[string]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

// lock takes the lock for key and returns the function that releases it. Locks
// are reference counted so that the map only holds the keys currently being
// written; an object store can otherwise accumulate one entry per key ever
// touched.
func (l *keyLocks) lock(key string) func() {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*keyLock)
	}
	entry, ok := l.locks[key]
	if !ok {
		entry = &keyLock{}
		l.locks[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}
