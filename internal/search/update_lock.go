package search

import "sync"

var (
	updateLocksMu sync.Mutex
	updateLocks   = make(map[string]*updateLock)
)

type updateLock struct {
	mu   sync.Mutex
	refs uint
}

// lockUpdate serializes index updates for the same parent while allowing
// unrelated directories to update concurrently.
func lockUpdate(parent string) func() {
	updateLocksMu.Lock()
	lock, ok := updateLocks[parent]
	if !ok {
		lock = &updateLock{}
		updateLocks[parent] = lock
	}
	lock.refs++
	updateLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()

		updateLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(updateLocks, parent)
		}
		updateLocksMu.Unlock()
	}
}
