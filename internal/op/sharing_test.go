package op_test

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

func newTestSharing(t *testing.T, maxAccessed int) *model.Sharing {
	t.Helper()
	s := &model.Sharing{
		SharingDB: &model.SharingDB{MaxAccessed: maxAccessed},
		Files:     []string{"/a"},
		Creator:   &model.User{Permission: 1 << 14},
	}
	if _, err := db.CreateSharing(s.SharingDB); err != nil {
		t.Fatal(err)
	}
	return s
}

func accessed(t *testing.T, id string) int {
	t.Helper()
	s, err := db.GetSharingById(id)
	if err != nil {
		t.Fatal(err)
	}
	return s.Accessed
}

func TestSharingAccessLimit(t *testing.T) {
	s := newTestSharing(t, 1)

	// browsing never takes a slot
	for range 3 {
		if err := op.CheckSharing("1.1.1.1", "", s); err != nil {
			t.Fatalf("check before download: %v", err)
		}
	}
	if n := accessed(t, s.ID); n != 0 {
		t.Fatalf("metadata took a slot: accessed = %d", n)
	}

	if err := op.AccessSharing("1.1.1.1", "", s); err != nil {
		t.Fatalf("first download: %v", err)
	}
	if n := accessed(t, s.ID); n != 1 {
		t.Fatalf("accessed = %d, want 1", n)
	}

	// #2143: the visitor holding the last slot keeps working
	s.Accessed = 1
	if err := op.CheckSharing("1.1.1.1", "", s); err != nil {
		t.Fatalf("slot holder check: %v", err)
	}
	if err := op.AccessSharing("1.1.1.1", "", s); err != nil {
		t.Fatalf("slot holder download: %v", err)
	}

	// new visitors are rejected
	if err := op.CheckSharing("2.2.2.2", "", s); !errors.Is(err, errs.InvalidSharing) {
		t.Fatalf("new visitor check: %v", err)
	}
	if err := op.AccessSharing("2.2.2.2", "", s); !errors.Is(err, errs.InvalidSharing) {
		t.Fatalf("new visitor download: %v", err)
	}
	if n := accessed(t, s.ID); n != 1 {
		t.Fatalf("accessed = %d, want 1", n)
	}
}

func TestSharingAccessUsesDatabaseState(t *testing.T) {
	s := newTestSharing(t, 1)

	// The in-memory sharing metadata can be stale while the database is authoritative.
	s.Accessed = 1
	if err := op.AccessSharing("3.3.3.3", "", s); err != nil {
		t.Fatalf("stale metadata rejected access: %v", err)
	}
	if n := accessed(t, s.ID); n != 1 {
		t.Fatalf("accessed = %d, want 1", n)
	}
}

func TestSharingAccessConcurrent(t *testing.T) {
	t.Run("distinct ips never exceed limit", func(t *testing.T) {
		s := newTestSharing(t, 3)
		var ok atomic.Int32
		var wg sync.WaitGroup
		for i := range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if op.AccessSharing(fmt.Sprintf("10.0.0.%d", i), "", s) == nil {
					ok.Add(1)
				}
			}()
		}
		wg.Wait()
		if ok.Load() != 3 || accessed(t, s.ID) != 3 {
			t.Fatalf("granted = %d, accessed = %d, want 3/3", ok.Load(), accessed(t, s.ID))
		}
	})

	t.Run("same ip counts once", func(t *testing.T) {
		s := newTestSharing(t, 1)
		var wg sync.WaitGroup
		for range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := op.AccessSharing("10.0.1.1", "", s); err != nil {
					t.Errorf("same ip rejected: %v", err)
				}
			}()
		}
		wg.Wait()
		if n := accessed(t, s.ID); n != 1 {
			t.Fatalf("accessed = %d, want 1", n)
		}
	})
}
