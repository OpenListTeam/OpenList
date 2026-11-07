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
	// flushTimeout bounds one flush: the batch is written off the request path,
	// but a flush is also triggered from it when the buffer fills, and a database
	// that stopped answering must not pin that reader for ever.
	flushTimeout = 5 * time.Second
)

// Two intervals are variables so that a test can shorten them; production wants
// the values below.
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
	last time.Time
	hits int64
}

type tracker struct {
	mu      sync.Mutex
	pending map[string]*sample    // path -> reads not yet written
	recent  map[string]time.Time  // path -> last hit counted, for hitInterval
	written map[string]time.Time  // path -> last value known to be stored
	once    sync.Once
}

var defaultTracker = &tracker{}

// Record notes that path was just read. It is cheap on purpose: it takes a lock,
// writes a map entry, and returns. The database is written later, in bulk.
func Record(path string) {
	defaultTracker.record(path)
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
	return setting.GetStr(conf.FileAccessTrack, "true") != "false"
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
	database := db.GetDb()
	if database == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	result := database.WithContext(ctx).Where("last_access < ?", before.UTC()).Delete(&model.FileAccess{})
	return result.RowsAffected, result.Error
}

func (t *tracker) record(path string) {
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
	var batch map[string]*sample
	for path, s := range t.pending {
		if last, ok := t.written[path]; ok && now.Sub(last) < writeInterval {
			continue
		}
		if batch == nil {
			batch = make(map[string]*sample, len(t.pending))
		}
		batch[path] = s
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
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	// The conflict update must not overwrite what a concurrent reader knows:
	// last_access only moves forward, hits accumulate, first_access is kept. MySQL
	// spells the proposed row VALUES(col), everybody else excluded.col.
	assignments := map[string]interface{}{
		"last_access":  gorm.Expr("excluded.last_access"),
		"first_access": gorm.Expr("COALESCE(file_access.first_access, excluded.first_access)"),
		"hits":         gorm.Expr("file_access.hits + excluded.hits"),
	}
	if database.Dialector.Name() == "mysql" {
		assignments = map[string]interface{}{
			"last_access":  gorm.Expr("VALUES(last_access)"),
			"first_access": gorm.Expr("COALESCE(first_access, VALUES(first_access))"),
			"hits":         gorm.Expr("hits + VALUES(hits)"),
		}
	}
	err := database.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "path_hash"}},
		DoUpdates: clause.Assignments(assignments),
	}).CreateInBatches(rows, 200).Error
	if err != nil {
		// Losing a sample is acceptable; failing the read that produced it is
		// not, and that moment is already over by the time the batch is written.
		log.Debugf("access: record file read: %v", err)
		return
	}

	t.mu.Lock()
	if t.written == nil || len(t.written) >= maxTracked {
		t.written = make(map[string]time.Time, len(batch))
	}
	for path, s := range batch {
		t.written[path] = s.last
	}
	t.mu.Unlock()
}
