package s3

import (
	"context"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	log "github.com/sirupsen/logrus"
)

const (
	// accessFlushInterval is how often buffered reads are written to the
	// database. A read must not cost a write: a ranged read of one object is
	// already a request, and a block device issues thousands of them, so the
	// tracker buffers in memory and writes in batches.
	accessFlushInterval = 30 * time.Second
	// accessWriteInterval is the resolution of the recorded time. Recording the
	// same object more often than this buys nothing: "last read" only has to be
	// accurate enough to separate a hot object from a cold one.
	accessWriteInterval = time.Hour
	// accessMaxPending bounds the buffer so that a burst of reads against many
	// objects cannot grow it without limit before the next flush.
	accessMaxPending = 20000
	// accessMaxTracked bounds the memory that remembers already written times.
	accessMaxTracked = 100000
	// accessFlushTimeout bounds one flush. The batch is written off the request
	// path, but a flush is also triggered from it when the buffer fills, and a
	// database that stopped answering must not pin that reader for ever.
	accessFlushTimeout = 5 * time.Second
)

// accessTracker buffers object reads so that recording the time of a read costs
// no database write on the request path.
type accessTracker struct {
	mu      sync.Mutex
	pending map[string]time.Time // path -> most recent read not yet written
	written map[string]time.Time // path -> most recent value known to be stored
	// deleted remembers objects deleted while a batch was in flight. The batch
	// was collected before the delete and its upsert then inserts the row
	// again, so a forgotten path has to be filtered out of a batch and, once
	// the write lands, removed from the table as well.
	//
	// The value is the moment of the delete, because the tombstone does not
	// only have to swallow the samples that predate it: a key can be uploaded
	// again, and a read of the new object is newer than the delete and must be
	// recorded. Keeping only the presence of the path made every later read of
	// a re-created object disappear.
	deleted map[string]time.Time
	once    sync.Once
}

// recordAccess notes that the object was just read. The write happens later, in
// bulk, and only when the recorded time is old enough to be worth updating.
func (b *s3Backend) recordAccess(name string) {
	if name == "" || !accessTrackingEnabled() {
		return
	}
	b.access.once.Do(func() { go b.flushAccessLoop() })

	now := time.Now()
	b.access.mu.Lock()
	if b.access.pending == nil {
		b.access.pending = make(map[string]time.Time, 64)
	}
	b.access.pending[name] = now
	full := len(b.access.pending) >= accessMaxPending
	b.access.mu.Unlock()

	if full {
		b.flushAccess()
	}
}

// accessTrackingEnabled reports whether reads are recorded. Being absent from
// the settings table means enabled: the feature is on for everybody, and only
// an explicit "false" turns it off.
func accessTrackingEnabled() bool {
	// A TypeBool setting reaches us as the string the admin UI stored, and
	// "False" or "0" would otherwise read as enabled.
	value := strings.ToLower(strings.TrimSpace(setting.GetStr(conf.S3AccessTrack, "true")))
	return value != "false" && value != "0" && value != "off" && value != "no"
}

// forget drops the buffered and remembered read time of an object. A delete has
// to call it: the upsert of a batch collected before the delete would otherwise
// insert the row again, leaving a record of an object that no longer exists.
func (t *accessTracker) forget(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, name)
	delete(t.written, name)
	if t.deleted == nil || len(t.deleted) >= accessMaxTracked {
		// Bounded like the other maps: the tombstone only has to outlive the
		// flush that is in flight right now.
		t.deleted = make(map[string]time.Time, 64)
	}
	t.deleted[name] = time.Now()
}

// present drops the tombstone of a key that exists again. Uploading an object
// proves the delete no longer applies, and the tombstone would otherwise swallow
// every later read of the new object and, once the batch in flight lands, delete
// the row that holds its user metadata.
func (t *accessTracker) present(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.deleted, name)
}

func (b *s3Backend) flushAccessLoop() {
	ticker := time.NewTicker(accessFlushInterval)
	defer ticker.Stop()
	for range ticker.C {
		b.flushAccess()
	}
}

// flushAccess writes the buffered reads, skipping objects that were already
// written recently, and one transaction per batch so that a remote database
// pays one round trip instead of one per object.
// warnAccessFlushOnce reports whether this flush error should be logged as a
// warning: the first time it appears, and again whenever the message changes.
// Repeating it on every flush would bury the log during a read-heavy period.
var (
	accessFlushErrMu   sync.Mutex
	accessFlushErrSeen string
)

func warnAccessFlushOnce(message string) bool {
	accessFlushErrMu.Lock()
	defer accessFlushErrMu.Unlock()
	if accessFlushErrSeen == message {
		return false
	}
	accessFlushErrSeen = message
	return true
}

func (b *s3Backend) flushAccess() {
	b.access.mu.Lock()
	batch := b.access.pending
	b.access.pending = nil
	now := time.Now()
	fresh := make(map[string]time.Time, len(batch))
	for name, at := range batch {
		if last, ok := b.access.written[name]; ok && now.Sub(last) < accessWriteInterval {
			continue
		}
		// A delete that happened while this batch was waiting has to win: its
		// row must not be written back. Only the samples the delete covers are
		// dropped, though: a read recorded after the delete belongs to a key
		// that was uploaded again, and dropping it left the new object looking
		// as if it had never been read.
		if deletedAt, gone := b.access.deleted[name]; gone && !at.After(deletedAt) {
			continue
		}
		fresh[name] = at
	}
	b.access.mu.Unlock()

	if len(fresh) == 0 {
		return
	}

	rows := make([]model.S3ObjectMetadata, 0, len(fresh))
	for name, at := range fresh {
		stamp := at.UTC()
		rows = append(rows, model.S3ObjectMetadata{
			Path:       name,
			LastAccess: &stamp,
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), accessFlushTimeout)
	defer cancel()
	// Only the access time is touched: a row that also carries user metadata or
	// the recorded ETag keeps them. The new time must not go backwards, though -
	// two flushes can be in flight and commit in either order, and the older
	// batch then reopened an hour-long hole in the record.
	//
	// The comparison names the existing row's column, and that name carries the
	// configured table prefix (x_ by default); a hardcoded table name turns every
	// flush into "no such column" on SQLite and PostgreSQL, which left the access
	// list empty on a default install. MySQL spells the proposed row
	// VALUES(col), everybody else excluded.col. If the name cannot be resolved the
	// plain assignment is used: a possibly backwards sample beats a record that
	// never lands.
	database := db.GetDb()
	proposed, current := "excluded.last_access", ""
	if database != nil && database.Dialector.Name() == "mysql" {
		proposed = "VALUES(last_access)"
	}
	if table, err := db.TableNameOf(database, &model.S3ObjectMetadata{}); err == nil {
		current = table + ".last_access"
		if database != nil && database.Dialector.Name() == "mysql" {
			// MySQL rejects a qualified name on the left of the comparison.
			current = "last_access"
		}
	}
	assignment := gorm.Expr(proposed)
	if current != "" {
		assignment = gorm.Expr(
			"CASE WHEN " + current + " IS NULL OR " + proposed + " > " + current + " THEN " + proposed + " ELSE " + current + " END")
	}
	if b.flushTestHook != nil {
		// Test hook: runs between collecting the batch and writing it, which is
		// exactly the window a concurrent delete slips into.
		b.flushTestHook()
	}
	err := withS3MetadataTransaction(ctx, func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key_hash"}},
			DoUpdates: clause.Assignments(map[string]interface{}{"last_access": assignment}),
		}).CreateInBatches(rows, 200).Error
	})
	if err != nil {
		// Losing a sample is acceptable; failing the read that produced it is
		// not, and the moment is already over by the time the batch is written.
		// A warning is still due: when the write cannot land at all (a wrong
		// column name, a missing grant) the access list silently goes empty, and
		// the tiering job then treats every object as never read.
		if warnAccessFlushOnce(err.Error()) {
			log.Warnf("s3: record object access: %v", err)
		} else {
			log.Debugf("s3: record object access: %v", err)
		}
		return
	}

	// A delete can also land while the batch is being written. Those rows were
	// just inserted by the upsert, so drop them now: nothing else would, since
	// the batch no longer knows them and the object is gone.
	b.access.mu.Lock()
	var resurrected []string
	for name, at := range fresh {
		// Same rule as when the batch was collected: a sample recorded after
		// the delete is a read of the object that replaced it and has to stay.
		if deletedAt, ok := b.access.deleted[name]; ok && !at.After(deletedAt) {
			resurrected = append(resurrected, name)
			delete(b.access.deleted, name)
		}
	}
	b.access.mu.Unlock()
	if len(resurrected) > 0 {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), accessFlushTimeout)
		defer cancel()
		if err := withS3MetadataTransaction(cleanupCtx, func(tx *gorm.DB) error {
			return tx.Where("path IN ?", resurrected).Delete(&model.S3ObjectMetadata{}).Error
		}); err != nil {
			log.Warnf("s3: drop access rows of objects deleted during a flush: %v", err)
		}
	}

	b.access.mu.Lock()
	if b.access.written == nil {
		b.access.written = make(map[string]time.Time, len(fresh))
	}
	if len(b.access.written) >= accessMaxTracked {
		b.access.written = make(map[string]time.Time, len(fresh))
	}
	for name, at := range fresh {
		// The remembered time only moves forward too, so a slower batch
		// finishing late cannot make the throttle skip a fresher read.
		if prev, ok := b.access.written[name]; !ok || at.After(prev) {
			b.access.written[name] = at
		}
	}
	b.access.mu.Unlock()
}
