package op

import (
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/OpenListTeam/go-cache"
)

func TestCheckSharingWaitsForAccessSlotPublication(t *testing.T) {
	s := &model.Sharing{
		SharingDB: &model.SharingDB{MaxAccessed: 1},
		Files:     []string{"/a"},
		Creator:   &model.User{Permission: 1 << 14},
	}
	if _, err := db.CreateSharing(s.SharingDB); err != nil {
		t.Fatal(err)
	}

	ip := random.String(16)
	key := sharingAccessKey(s.ID, ip)
	started := make(chan struct{})
	release := make(chan struct{})
	accessDone := make(chan struct{})
	go func() {
		_, _, _ = sharingAccessG.Do(key, func() (struct{}, error) {
			close(started)
			<-release
			sharingAccessCache.Set(key, struct{}{}, cache.WithEx[struct{}](sharingAccessCountDelay))
			return struct{}{}, nil
		})
		close(accessDone)
	}()
	<-started

	result := make(chan error, 1)
	s.Accessed = 1
	go func() {
		result <- CheckSharing(ip, "", s)
	}()
	select {
	case err := <-result:
		t.Fatalf("check returned before access cache publication: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("check after access cache publication: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("check did not complete after access cache publication")
	}
	<-accessDone
}
