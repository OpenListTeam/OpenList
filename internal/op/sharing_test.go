package op

import (
	"fmt"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestSharingAccessLimit(t *testing.T) {
	for _, limit := range []int{1, 3} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			s := &model.Sharing{
				SharingDB: &model.SharingDB{
					ID:          t.Name(),
					FilesRaw:    `["/file"]`,
					MaxAccessed: limit,
				},
				Files:   []string{"/file"},
				Creator: &model.User{Permission: 1 << 14},
			}
			if _, err := db.CreateSharing(s.SharingDB); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.DeleteSharingById(s.ID); err != nil {
					t.Error(err)
				}
			})

			for visit := 1; visit <= limit; visit++ {
				ip := fmt.Sprintf("192.0.2.%d", visit)
				key := fmt.Sprintf("%s:%s", s.ID, ip)
				t.Cleanup(func() { sharingAccessCache.Del(key) })
				if !IsSharingValid(ip, s) {
					t.Fatalf("visit %d should be admitted", visit)
				}
				if err := CountSharingAccess(ip, s); err != nil {
					t.Fatal(err)
				}
				// Reload the persisted count as subsequent HTTP requests do.
				stored, err := db.GetSharingById(s.ID)
				if err != nil {
					t.Fatal(err)
				}
				s.SharingDB = stored
				if s.Accessed != visit {
					t.Fatalf("accessed = %d, want %d", s.Accessed, visit)
				}
				if !IsSharingValid(ip, s) {
					t.Fatalf("visit %d must be able to continue after being counted", visit)
				}
				if err := CountSharingAccess(ip, s); err != nil {
					t.Fatal(err)
				}
				if s.Accessed != visit {
					t.Fatal("a repeat request was counted again")
				}
			}

			if IsSharingValid("192.0.2.100", s) || IsSharingValid("", s) || s.Valid() {
				t.Fatal("new visits must be rejected once the limit is reached")
			}
			other := *s.SharingDB
			other.ID += "-other"
			otherShare := &model.Sharing{SharingDB: &other, Files: s.Files, Creator: s.Creator}
			if IsSharingValid("192.0.2.1", otherShare) {
				t.Fatal("an access record must not apply to another share")
			}

			key := fmt.Sprintf("%s:192.0.2.1", s.ID)
			if !sharingAccessCache.ExpireAt(key, time.Now().Add(-time.Minute)) {
				t.Fatal("expected an existing access record")
			}
			if IsSharingValid("192.0.2.1", s) {
				t.Fatal("an expired access record must not bypass the limit")
			}
		})
	}
}
