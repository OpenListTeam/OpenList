package access

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// A sample held back by the write throttle stays in the buffer, so the buffer can
// hold far more entries than one flush should walk. The scan has to be bounded:
// otherwise every Record past maxPending pays for the whole buffer while holding
// the lock that every other read also needs.
func TestFlushScanIsBounded(t *testing.T) {
	prepare(t)
	tr := defaultTracker
	now := time.Now().UTC()
	tr.mu.Lock()
	tr.pending = make(map[string]*sample, maxFlushScan+200)
	for i := 0; i < maxFlushScan+200; i++ {
		path := "/bounded/" + time.Duration(i).String() + ".bin"
		tr.pending[path] = &sample{last: now, hits: 1}
	}
	buffered := len(tr.pending)
	tr.mu.Unlock()

	tr.flush()

	tr.mu.Lock()
	left := len(tr.pending)
	written := len(tr.written)
	tr.mu.Unlock()
	if written > maxFlushScan {
		t.Fatalf("one flush wrote %d samples, more than the %d it may scan", written, maxFlushScan)
	}
	if left != buffered-written {
		t.Fatalf("buffer holds %d rows after writing %d of %d", left, written, buffered)
	}
	var stored int64
	if err := db.GetDb().Model(&model.FileAccess{}).Count(&stored).Error; err != nil {
		t.Fatalf("count file_access: %v", err)
	}
	if int(stored) != written {
		t.Fatalf("table holds %d rows, want %d", stored, written)
	}

	// Draining does not need a special path: the next flush takes the next slice.
	for i := 0; i < 2 && left > 0; i++ {
		tr.flush()
		tr.mu.Lock()
		left = len(tr.pending)
		tr.mu.Unlock()
	}
	if left != 0 {
		t.Fatalf("%d samples still buffered after three flushes", left)
	}
}

// Two flushes can commit in either order. The older one must not move the stored
// time back: a regression there reopens an hour-long hole in the record and makes
// a hot file look cold.
func TestAccessTimeOnlyMovesForward(t *testing.T) {
	prepare(t)
	tr := defaultTracker
	path := "/bounded/forward.bin"
	newest := time.Now().UTC().Truncate(time.Second)
	older := newest.Add(-time.Hour)

	tr.mu.Lock()
	tr.pending = map[string]*sample{path: {last: newest, hits: 1}}
	tr.mu.Unlock()
	tr.flush()

	stored := row(t, path)
	if stored == nil || stored.LastAccess == nil {
		t.Fatal("the first flush did not store the read")
	}
	if !stored.LastAccess.Equal(newest) {
		t.Fatalf("stored %v, want %v", stored.LastAccess, newest)
	}

	// A batch carrying an older stamp for the same path, as a slower flush that
	// started before the first one would.
	tr.mu.Lock()
	tr.pending = map[string]*sample{path: {last: older, hits: 1}}
	delete(tr.written, path)
	tr.mu.Unlock()
	tr.flush()

	stored = row(t, path)
	if stored == nil || stored.LastAccess == nil {
		t.Fatal("the second flush removed the record")
	}
	if !stored.LastAccess.Equal(newest) {
		t.Fatalf("stored time went back to %v, want %v", stored.LastAccess, newest)
	}
	if stored.Hits != 2 {
		t.Fatalf("hits = %d, want 2", stored.Hits)
	}
}

// recordingLogger collects the SQL a test causes.
type recordingLogger struct {
	gormlogger.Interface
	mu   sync.Mutex
	seen []string
}

func (l *recordingLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.mu.Lock()
	l.seen = append(l.seen, sql)
	l.mu.Unlock()
}

func (l *recordingLogger) LogMode(gormlogger.LogLevel) gormlogger.Interface { return l }

// Prune used to run one unbounded DELETE under the 5s flush timeout, so a table
// of any size cancelled the statement and pruned nothing. It now deletes bounded
// batches under a budget of its own - the statements are what the test watches.
func TestPruneDeletesInBatches(t *testing.T) {
	prepare(t)
	old := time.Now().UTC().AddDate(0, 0, -400)
	rows := make([]model.FileAccess, 0, pruneBatch+25)
	for i := 0; i < pruneBatch+25; i++ {
		stamp := old
		rows = append(rows, model.FileAccess{
			Path:       "/prune/" + time.Duration(i).String() + ".bin",
			LastAccess: &stamp,
		})
	}
	if err := db.GetDb().CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("seed file_access: %v", err)
	}
	// One row that must survive.
	fresh := time.Now().UTC()
	if err := db.GetDb().Create(&model.FileAccess{Path: "/prune/fresh.bin", LastAccess: &fresh}).Error; err != nil {
		t.Fatalf("seed fresh row: %v", err)
	}

	recorder := &recordingLogger{Interface: gormlogger.Discard}
	instrumented, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{Logger: recorder})
	if err != nil {
		t.Fatalf("open instrumented database: %v", err)
	}
	previous := db.GetDb()
	db.Init(instrumented)
	t.Cleanup(func() { db.Init(previous) })

	deleted, err := Prune(time.Now().UTC().AddDate(0, 0, -1))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != int64(len(rows)) {
		t.Fatalf("pruned %d rows, want %d", deleted, len(rows))
	}
	if row(t, "/prune/fresh.bin") == nil {
		t.Fatal("prune removed a row that is still read")
	}

	// One statement covering every row is exactly the shape that timed out.
	deletes := 0
	for _, sql := range recorder.seen {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "DELETE") {
			continue
		}
		deletes++
		if strings.Count(sql, "?") > pruneBatch {
			t.Fatalf("prune issued %d placeholders in one statement (max %d): %s",
				strings.Count(sql, "?"), pruneBatch, sql)
		}
	}
	if deletes < 2 {
		t.Fatalf("prune issued %d DELETE statements, want at least 2 bounded batches", deletes)
	}
}
