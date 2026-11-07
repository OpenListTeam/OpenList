package s3

import (
	"context"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
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
	return setting.GetStr(conf.S3AccessTrack, "true") != "false"
}

// forget drops the buffered and remembered read time of an object. A delete has
// to call it: the upsert of a batch collected before the delete would otherwise
// insert the row again, leaving a record of an object that no longer exists.
func (t *accessTracker) forget(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, name)
	delete(t.written, name)
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
	err := withS3MetadataTransaction(ctx, func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "key_hash"}},
			// Only the access time is touched: a row that also carries user
			// metadata or the recorded ETag keeps them.
			DoUpdates: clause.AssignmentColumns([]string{"last_access"}),
		}).CreateInBatches(rows, 200).Error
	})
	if err != nil {
		// Losing a sample is acceptable; failing the read that produced it is
		// not, and the moment is already over by the time the batch is written.
		log.Debugf("s3: record object access: %v", err)
		return
	}

	b.access.mu.Lock()
	if b.access.written == nil {
		b.access.written = make(map[string]time.Time, len(fresh))
	}
	if len(b.access.written) >= accessMaxTracked {
		b.access.written = make(map[string]time.Time, len(fresh))
	}
	for name, at := range fresh {
		b.access.written[name] = at
	}
	b.access.mu.Unlock()
}
