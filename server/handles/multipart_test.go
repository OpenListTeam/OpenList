package handles

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/multipart"
)

// A session that was never found has nothing to report: its error response
// carries null data, as documented, rather than an all-zero snapshot.
func TestSnapshotDataOmitsUnknownSession(t *testing.T) {
	if got := snapshotData(multipart.SessionSnapshot{}); got != nil {
		t.Fatalf("snapshotData(zero snapshot) = %#v, want nil", got)
	}
	snap := multipart.SessionSnapshot{ID: "3f2c9e2a", State: multipart.StateFailedRetriable}
	got, ok := snapshotData(snap).(multipart.SessionSnapshot)
	if !ok || got.ID != snap.ID || got.State != snap.State {
		t.Fatalf("snapshotData(known snapshot) = %#v, want the snapshot itself", snapshotData(snap))
	}
}
