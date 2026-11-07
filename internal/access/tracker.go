// Package access records when a file was read, and how often.
//
// The storage drivers keep a modification time but no access time: a backup
// written once and a movie watched every night look identical to them. A
// hot/cold split cannot be built on that, because the file that must stay hot is
// exactly the one being read - and a modification time goes stale the moment the
// file stops changing.
//
// "Was this read recently" is known only where content leaves the instance:
// the direct link (/d), the proxy (/p), WebDAV and the media library, which
// hands out /d and /p links. Those points call Record; the storage driver is
// never involved, so no driver has to expose anything it does not have.
package access

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
	// flushInterval is how often buffered reads reach the database. A read must
	// not cost a write: one playback already produces many requests, and a
	// client that seeks produces many more.
	flushInterval = 30 * time.Second
	// maxPending bounds the buffer, so that a burst of reads against many files
	// cannot grow it without limit before the next flush.
	maxPending = 20000
	// maxTracked bounds the memory that remembers already written times.
	maxTracked = 100000
	// pruneBatch bounds one DELETE of Prune. A row is small and the statement
	// runs off the request path, so the batch is generous but not unbounded.
	pruneBatch = 5000
	// pruneBudget is how long one Prune call may spend deleting.
	pruneBudget = time.Minute
	// maxFlushScan bounds how many buffered samples one flush examines. Samples
	// held back by the write throttle stay in the buffer, so without a bound the
	// scan grew with the library and every Record past the buffer limit paid for
	// it - measured at ~690us per read with 20k pending. Successive flushes
	// (every flushInterval) drain the rest, so capping costs nothing but a little
	// delay on samples nobody is waiting for.
	maxFlushScan = 4096
	// flushTimeout bounds one flush: the batch is written off the request path,
	// but a flush is also triggered from it when the buffer fills, and a database
	// that stopped answering must not pin that reader for ever.
	flushTimeout = 5 * time.Second
)

// Two intervals are variables so that a test can shorten them; production wants
// the values below.
// warnFlushErrorOnce reports whether this flush error should be logged as a
// warning: the first time it appears, and again whenever the message changes,
// mirroring server/s3/access_tracker.go. Repeating it on every flush would
// bury the log during a read-heavy period.
var (
	accessFlushErrMu   sync.Mutex
	accessFlushErrSeen string
)

func warnFlushErrorOnce(message string) bool {
	accessFlushErrMu.Lock()
	defer accessFlushErrMu.Unlock()
	if accessFlushErrSeen == message {
		return false
	}
	accessFlushErrSeen = message
	return true
}

var (
	// writeInterval is the resolution of the recorded time. "Last read" only has
	// to be accurate enough to separate a file read this week from one read two
	// months ago, so the same file is not written more than once an hour.
	writeInterval = time.Hour
	// hitInterval merges the requests of one playback into one hit: a player
	// asks for ranges of the same file dozens of times, and counting each would
	// turn any file touched once into a popular one.
	hitInterval = 10 * time.Minute
)

// sample is what is remembered about one path between flushes.
type sample struct {
	last   time.Time
	hits   int64
	bucket string
}

type tracker struct {
	mu      sync.Mutex
	pending map[string]*sample   // path -> reads not yet written
	recent  map[string]time.Time // path -> last hit counted, for hitInterval
	written map[string]time.Time // path -> last value known to be stored
	// forgotten counts deletions per path. A batch takes the count for the paths
	// it carries before writing them, so a delete that lands while the batch is
	// in flight - after Forget could still reach the buffer - is visible as a
	// count that moved, and the row is dropped once the write is done.
	forgotten map[string]uint64
	once      sync.Once
}

var defaultTracker = &tracker{}

// Record notes that path was just read. It is cheap on purpose: it takes a lock,
// writes a map entry, and returns. The database is written later, in bulk.
func Record(path string) {
	defaultTracker.record("", path)
}

// RecordBucket notes a read that came through a named S3 bucket. The bucket is
// kept as an attribute of the path, so that "which bucket is this file hot in"
// is answered by the row itself instead of by matching storage paths: two buckets
// can front the same library, and an alias mount makes their paths identical.
func RecordBucket(bucket, path string) {
	defaultTracker.record(bucket, path)
}

// Forget drops everything remembered about a path. Deleting the file has to call
// it: a batch collected before the delete would otherwise insert the row again
// and leave a record of a file that no longer exists.
func Forget(path string) {
	defaultTracker.forget(path)
}

// Flush writes the buffer now instead of waiting for the next tick. Tests use it;
// a caller that is about to move files may too, so that the decision is made on
// fresh numbers.
func Flush() {
	defaultTracker.flush()
}

// Enabled reports whether reads are recorded. An absent setting means enabled:
// the feature is on for everybody, and only an explicit "false" turns it off.
func Enabled() bool {
	// The setting is a TypeBool stored as text: "False" or "0" have to switch
	// recording off just like "false" does.
	value := strings.ToLower(strings.TrimSpace(setting.GetStr(conf.FileAccessTrack, "true")))
	return value != "false" && value != "0" && value != "off" && value != "no"
}

// Prune deletes rows whose last read is older than the cutoff and reports how
// many went.
//
// The table holds one row per file this instance has ever served, not one row
// per request, so it grows with the library rather than with traffic - but it
// still grows: a file deleted or renamed leaves its row behind, and a library
// re-imported every year would accumulate a row per incarnation. A cutoff keeps
// the table proportional to what is still being read, at the price of forgetting
// how often something was read before the cutoff (the next read starts at 1).
func Prune(before time.Time) (int64, error) {
	return PruneDays(daysBefore(before))
}

// daysBefore converts a cutoff instant into a whole number of days in the past,
// for the callers that describe a prune as "everything older than this instant".
func daysBefore(before time.Time) int {
	days := int(time.Since(before.UTC()).Hours() / 24)
	if days < 1 {
		return 1
	}
	return days
}

// PruneDays drops the rows whose last read is older than the cutoff, in bounded
// batches, and reports how many went. One cutoff is enough now that the table no
// longer distinguishes tiers: the cutoff exists to keep the table proportional to
// what is still being read, and the only cost of forgetting an old row is that the
// file's read count restarts at one.
func PruneDays(days int) (int64, error) {
	if days < 1 {
		return 0, nil
	}
	database := db.GetDb()
	if database == nil {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	return pruneScope(database, func(q *gorm.DB) *gorm.DB {
		return q.Where("last_access < ?", cutoff)
	})
}

// CountPrunableDays reports how many rows PruneDays(days) would drop, without
// deleting anything: an operator about to prune on the wrong cutoff needs to see
// the number before the rows are gone.
func CountPrunableDays(days int) (int64, error) {
	if days < 1 {
		return 0, nil
	}
	database := db.GetDb()
	if database == nil {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	var n int64
	err := database.Model(&model.FileAccess{}).Where("last_access < ?", cutoff).Count(&n).Error
	return n, err
}

// pruneScope deletes the rows a scope selects, in batches, under one budget.
func pruneScope(database *gorm.DB, scope func(*gorm.DB) *gorm.DB) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pruneBudget)
	defer cancel()
	var total int64
	for {
		var ids []uint
		err := scope(database.WithContext(ctx).Model(&model.FileAccess{})).
			Limit(pruneBatch).
			Pluck("id", &ids).Error
		if err != nil {
			if ctx.Err() != nil {
				// The budget ran out mid-way. Report what went instead of claiming
				// failure: everything still selected is picked up by the next call.
				log.Warnf("access: prune stopped after %d row(s) (%v); the rest stays for the next run", total, ctx.Err())
				return total, nil
			}
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		// An explicit id list, not a subquery: MySQL refuses LIMIT inside the
		// subquery of a DELETE. The scope is applied again so that the DELETE
		// matches what is stale now rather than what was stale when the ids were
		// read: a flush can commit in between, and the row of a file read at this
		// very moment must not go - it is the only evidence that file is in use.
		res := scope(database.WithContext(ctx).Model(&model.FileAccess{})).
			Where("id IN ?", ids).
			Delete(&model.FileAccess{})
		if res.Error != nil {
			if ctx.Err() != nil {
				log.Warnf("access: prune stopped after %d row(s) (%v); the rest stays for the next run", total, ctx.Err())
				return total, nil
			}
			return total, res.Error
		}
		total += res.RowsAffected
		if len(ids) < pruneBatch {
			return total, nil
		}
	}
}

func (t *tracker) record(bucket, path string) {
	if path == "" || !Enabled() {
		return
	}
	t.once.Do(func() { go t.loop() })

	now := time.Now().UTC()
	t.mu.Lock()
	if t.pending == nil {
		t.pending = make(map[string]*sample, 64)
	}
	s := t.pending[path]
	if s == nil {
		s = &sample{}
		t.pending[path] = s
	}
	s.last = now
	// The bucket is an attribute, not part of the key: the row is per file, and
	// the file is the same one whichever bucket served it. An unnamed read does
	// not clear what a named one recorded.
	if bucket != "" {
		s.bucket = bucket
	}
	if t.recent == nil {
		t.recent = make(map[string]time.Time, 64)
	}
	if prev, ok := t.recent[path]; !ok || now.Sub(prev) >= hitInterval {
		s.hits++
		if len(t.recent) >= maxTracked {
			t.recent = make(map[string]time.Time, 64)
		}
		t.recent[path] = now
	}
	full := len(t.pending) >= maxPending
	t.mu.Unlock()

	if full {
		t.flush()
	}
}

func (t *tracker) forget(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, path)
	delete(t.recent, path)
	delete(t.written, path)
	// Remember that this path was deleted at this point: a batch that already
	// left the buffer cannot be recalled, so the flush drops its row afterwards.
	if t.forgotten == nil || len(t.forgotten) >= maxTracked {
		t.forgotten = make(map[string]uint64, 64)
	}
	t.forgotten[path]++
}

func (t *tracker) loop() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for range ticker.C {
		t.flush()
	}
}

// flush writes the buffered reads, skipping paths that were written less than
// writeInterval ago and writing the rest in one statement per batch. A skipped
// path keeps its sample, so its hits are not lost - they are added on the write
// that follows.
func (t *tracker) flush() {
	database := db.GetDb()
	if database == nil {
		return
	}

	t.mu.Lock()
	now := time.Now().UTC()
	// The scan is capped: the buffer may hold far more samples than one flush
	// should examine, because a sample held back by the write throttle stays
	// where it is. Sizing the batch by the buffer instead of by the scan was
	// another way to pay the same cost.
	scan := len(t.pending)
	if scan > maxFlushScan {
		scan = maxFlushScan
	}
	batch := make(map[string]*sample, scan)
	// The deletion counter each path had when the batch took it: anything that
	// moves while the batch is being written is a delete of that path.
	generations := make(map[string]uint64, scan)
	for path, s := range t.pending {
		if len(batch) >= scan {
			break
		}
		if last, ok := t.written[path]; ok && now.Sub(last) < writeInterval {
			continue
		}
		batch[path] = s
		generations[path] = t.forgotten[path]
		delete(t.pending, path)
	}
	t.mu.Unlock()

	if len(batch) == 0 {
		return
	}

	rows := make([]model.FileAccess, 0, len(batch))
	for path, s := range batch {
		stamp := s.last
		first := stamp
		rows = append(rows, model.FileAccess{
			Path:        path,
			LastAccess:  &stamp,
			FirstAccess: &first,
			Hits:        s.hits,
			Bucket:      s.bucket,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	// The conflict update must not overwrite what a concurrent reader knows:
	// last_access only moves forward, hits accumulate, first_access is kept. MySQL
	// spells the proposed row VALUES(col), everybody else excluded.col.
	// The comparison names the existing row's column, and that name carries the
	// configured table prefix: spelling the table out by hand breaks the whole
	// statement as soon as a prefix is configured.
	table, err := db.TableNameOf(database, &model.FileAccess{})
	if err != nil {
		table = ""
	}
	current := func(column string) string {
		if table == "" {
			return column
		}
		return table + "." + column
	}
	assignments := map[string]interface{}{
		"last_access":  gorm.Expr("CASE WHEN " + current("last_access") + " IS NULL OR excluded.last_access > " + current("last_access") + " THEN excluded.last_access ELSE " + current("last_access") + " END"),
		"first_access": gorm.Expr("COALESCE(" + current("first_access") + ", excluded.first_access)"),
		"hits":         gorm.Expr(current("hits") + " + excluded.hits"),
		// The bucket of the last read that named one: an unnamed reader (/d, /p,
		// WebDAV) must not erase the attribution S3 recorded.
		"bucket": gorm.Expr("CASE WHEN excluded.bucket IS NOT NULL AND excluded.bucket <> '' THEN excluded.bucket ELSE COALESCE(" + current("bucket") + ", '') END"),
	}
	if database.Dialector.Name() == "mysql" {
		assignments = map[string]interface{}{
			"last_access":  gorm.Expr("CASE WHEN last_access IS NULL OR VALUES(last_access) > last_access THEN VALUES(last_access) ELSE last_access END"),
			"first_access": gorm.Expr("COALESCE(first_access, VALUES(first_access))"),
			"hits":         gorm.Expr("hits + VALUES(hits)"),
			"bucket":       gorm.Expr("CASE WHEN VALUES(bucket) IS NOT NULL AND VALUES(bucket) <> '' THEN VALUES(bucket) ELSE COALESCE(bucket, '') END"),
		}
	}
	err = database.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "path_hash"}},
		DoUpdates: clause.Assignments(assignments),
	}).CreateInBatches(rows, 200).Error
	if err != nil {
		// Losing a sample is acceptable; failing the read that produced it is
		// not, and that moment is already over by the time the batch is written.
		// The first failure of each kind still surfaces as a warning: tiering
		// decisions read this table, and a database that stays unwritable must
		// not hide behind debug-level logging.
		if warnFlushErrorOnce(err.Error()) {
			log.Warnf("access: record file read: %v (further identical failures are logged at debug level only)", err)
		} else {
			log.Debugf("access: record file read: %v", err)
		}
		return
	}

	t.mu.Lock()
	if t.written == nil || len(t.written) >= maxTracked {
		t.written = make(map[string]time.Time, len(batch))
	}
	// Rows of files deleted while this batch was being written: the sample was
	// already out of the buffer when Forget ran, so the insert above brought the
	// row back and nothing else would ever remove it - the table would keep a
	// row for a file that does not exist, which the tiering job cannot act on.
	forgotten := make([]forgottenRow, 0, 1)
	for path, s := range batch {
		// Forward only: two flushes can commit out of order, and remembering the
		// older stamp would make the throttle skip a fresher read.
		if prev, ok := t.written[path]; !ok || s.last.After(prev) {
			t.written[path] = s.last
		}
		if t.forgotten[path] != generations[path] {
			forgotten = append(forgotten, forgottenRow{path: path, stamp: s.last})
		}
	}
	t.mu.Unlock()

	if len(forgotten) > 0 {
		t.dropForgotten(database, forgotten)
	}
}

// forgottenRow is one row a batch wrote for a path that was deleted meanwhile.
type forgottenRow struct {
	path  string
	stamp time.Time
}

// dropForgotten deletes the rows written for paths deleted mid-flight. The
// last_access bound is what keeps the repair from undoing a real read: a file
// deleted and then recreated and read again has a later stamp, and that row has
// to survive - it is the evidence the new file is in use.
func (t *tracker) dropForgotten(database *gorm.DB, rows []forgottenRow) {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	for _, r := range rows {
		err := database.WithContext(ctx).
			Where("path_hash = ? AND last_access <= ?", model.HashFilePath(r.path), r.stamp).
			Delete(&model.FileAccess{}).Error
		if err != nil {
			log.Debugf("access: drop row of a deleted file: %v", err)
			return
		}
	}
}
