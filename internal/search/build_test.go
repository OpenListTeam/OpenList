package search

import (
	"testing"
	"time"
)

func TestLockUpdateSerializesSameParent(t *testing.T) {
	unlockFirst := lockUpdate("/same-parent")
	secondStarted := make(chan struct{})
	secondAcquired := make(chan struct{})
	secondReleased := make(chan struct{})
	go func() {
		close(secondStarted)
		unlockSecond := lockUpdate("/same-parent")
		close(secondAcquired)
		unlockSecond()
		close(secondReleased)
	}()
	<-secondStarted

	select {
	case <-secondAcquired:
		t.Fatal("second update acquired the same parent lock")
	case <-time.After(20 * time.Millisecond):
	}

	unlockFirst()
	select {
	case <-secondReleased:
	case <-time.After(time.Second):
		t.Fatal("second update did not acquire the released parent lock")
	}

	updateLocksMu.Lock()
	defer updateLocksMu.Unlock()
	if len(updateLocks) != 0 {
		t.Fatalf("update locks were not cleaned up: %d", len(updateLocks))
	}
}

func TestLockUpdateAllowsDifferentParents(t *testing.T) {
	unlockFirst := lockUpdate("/first-parent")
	defer unlockFirst()

	secondAcquired := make(chan struct{})
	go func() {
		unlockSecond := lockUpdate("/second-parent")
		unlockSecond()
		close(secondAcquired)
	}()

	select {
	case <-secondAcquired:
	case <-time.After(time.Second):
		t.Fatal("update for a different parent was blocked")
	}
}
