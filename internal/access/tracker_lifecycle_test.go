package access

import (
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// The buffer is written in bulk off the request path, so "the sample left the
// buffer" and "the row is in the table" are two different moments. Deletion
// (Forget) and Prune both act on the table, and a read that is in flight between
// those two moments is exactly the one they race with. These tests drive the two
// windows deterministically with a GORM callback that stops the statement the
// window opens on.

// holdCreate stops the first INSERT into file_access until release is closed,
// and reports through entered that the batch has left the in-memory buffer.
func holdCreate(t *testing.T) (entered chan struct{}, release func()) {
	t.Helper()
	inWrite := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	name := "audit:hold file_access insert"
	err := db.GetDb().Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "file_access" {
			return
		}
		once.Do(func() {
			close(inWrite)
			<-releaseCh
		})
	})
	if err != nil {
		t.Fatalf("register create hook: %v", err)
	}
	t.Cleanup(func() { _ = db.GetDb().Callback().Create().Remove(name) })
	var relOnce sync.Once
	return inWrite, func() { relOnce.Do(func() { close(releaseCh) }) }
}

// A file deleted while its read is still in the write batch must not come back.
// Forget is what the delete path calls (server/s3/backend.go) for exactly this
// reason: a row for a file that no longer exists is an instruction to the
// tiering job to move nothing, and it never expires on its own.
func TestForgetDropsReadDeletedDuringFlush(t *testing.T) {
	prepare(t)
	const path = "/本机储存/热层/deleted.mkv"

	Record(path)

	entered, release := holdCreate(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		Flush()
	}()
	<-entered // the batch has left the buffer and is being written
	Forget(path)
	release()
	<-done

	if r := row(t, path); r != nil {
		t.Fatalf("the in-flight batch recreated the row of a deleted file: path=%q hits=%d", r.Path, r.Hits)
	}
}

// A read that commits between the SELECT and the DELETE of one prune batch must
// not be deleted: the row is the only evidence that the file is in use, and
// losing it makes the tiering job treat an actively read file as never read.
func TestPruneKeepsRowReadWhilePruneIsDeleting(t *testing.T) {
	prepare(t)
	const path = "/本机储存/冷层/became-hot.bin"

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -400)
	stale := model.FileAccess{Path: path, LastAccess: &old, FirstAccess: &old, Hits: 5}
	if err := db.GetDb().Create(&stale).Error; err != nil {
		t.Fatalf("insert stale row: %v", err)
	}

	// A flush of a real read of that same file committing between the id list
	// and the DELETE: it pushes last_access to now, which is what makes the row
	// no longer prunable.
	var once sync.Once
	name := "audit:read during prune"
	err := db.GetDb().Callback().Delete().Before("gorm:delete").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "file_access" {
			return
		}
		once.Do(func() {
			fresh := time.Now().UTC()
			// Same connection as the DELETE: the point is the ordering, not the
			// isolation level - the row must look freshly read by the time the
			// DELETE decides what to match.
			if err := tx.Session(&gorm.Session{NewDB: true}).Model(&model.FileAccess{}).
				Where("path_hash = ?", model.HashFilePath(path)).
				Update("last_access", fresh).Error; err != nil {
				t.Errorf("simulate the concurrent read: %v", err)
			}
		})
	})
	if err != nil {
		t.Fatalf("register delete hook: %v", err)
	}
	t.Cleanup(func() { _ = db.GetDb().Callback().Delete().Remove(name) })

	deleted, err := Prune(now.AddDate(0, 0, -365))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if r := row(t, path); r == nil {
		t.Fatalf("prune deleted a row that was read while the batch was being deleted (deleted=%d)", deleted)
	}
}
