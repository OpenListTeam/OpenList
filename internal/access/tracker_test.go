package access

import (
	"os"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// TestMain gives the package a real database: what is recorded here outlives the
// process, and only a database can show that.
func TestMain(m *testing.M) {
	database, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		panic(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		panic(err)
	}
	// A shared in-memory database exists only while a connection is open, and one
	// connection keeps every test on the same copy of it.
	sqlDB.SetMaxOpenConns(1)
	// db.Init reads the database type from the configuration, which a test binary
	// never parses.
	conf.Conf = &conf.Config{}
	db.Init(database)
	os.Exit(m.Run())
}

// The intervals below are package globals that some tests shorten to make a
// throttle observable. Captured before any test runs, so prepare can put the
// real values back: a test that runs after one of those has to see the shipped
// defaults, not whatever the previous test left behind.
var (
	defaultHitInterval   = hitInterval
	defaultWriteInterval = writeInterval
)

// prepare gives a test a clean buffer and a clean table.
func prepare(t *testing.T) {
	t.Helper()
	hitInterval, writeInterval = defaultHitInterval, defaultWriteInterval
	t.Cleanup(func() { hitInterval, writeInterval = defaultHitInterval, defaultWriteInterval })
	if err := db.GetDb().Where("1 = 1").Delete(&model.FileAccess{}).Error; err != nil {
		t.Fatalf("clear file_access: %v", err)
	}
	defaultTracker = &tracker{}
	t.Cleanup(func() { defaultTracker = &tracker{} })
}

func row(t *testing.T, path string) *model.FileAccess {
	t.Helper()
	var found model.FileAccess
	err := db.GetDb().Where("path_hash = ?", model.HashFilePath(path)).Take(&found).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		t.Fatalf("load %q: %v", path, err)
	}
	return &found
}

func setEnabled(t *testing.T, value string) {
	t.Helper()
	item := &model.SettingItem{
		Key:   conf.FileAccessTrack,
		Value: value,
		Type:  conf.TypeBool,
		Group: model.GLOBAL,
	}
	if err := db.SaveSettingItem(item); err != nil {
		t.Fatalf("save %s: %v", conf.FileAccessTrack, err)
	}
	op.SettingCacheUpdate()
	t.Cleanup(func() {
		if err := db.GetDb().Where("key = ?", conf.FileAccessTrack).Delete(&model.SettingItem{}).Error; err != nil {
			t.Fatalf("delete %s: %v", conf.FileAccessTrack, err)
		}
		op.SettingCacheUpdate()
	})
}

// A read must not cost a write on the request path: it is buffered, then written
// in bulk.
func TestRecordIsBufferedThenWritten(t *testing.T) {
	prepare(t)
	const path = "/本机储存/热层/movie.mkv"

	Record(path)
	if r := row(t, path); r != nil {
		t.Fatalf("a read must not write on the request path, found %+v", r)
	}

	Flush()
	r := row(t, path)
	if r == nil {
		t.Fatal("flush wrote no row")
	}
	if r.LastAccess == nil || r.FirstAccess == nil {
		t.Fatalf("row without times: %+v", r)
	}
	if r.Hits != 1 {
		t.Fatalf("hits = %d, want 1", r.Hits)
	}
}

// The requests of one playback are one hit, otherwise a single range request
// would make any file look popular.
func TestOnePlaybackIsOneHit(t *testing.T) {
	prepare(t)
	const path = "/本机储存/热层/movie.mkv"

	for i := 0; i < 20; i++ {
		Record(path)
	}
	Flush()
	if r := row(t, path); r.Hits != 1 {
		t.Fatalf("hits = %d, want 1: a playback is one hit", r.Hits)
	}
}

// After the hit interval has passed, a new read is a new hit.
func TestSecondSessionIsSecondHit(t *testing.T) {
	prepare(t)
	const path = "/本机储存/热层/movie.mkv"

	oldHit, oldWrite := hitInterval, writeInterval
	hitInterval, writeInterval = 0, 0
	t.Cleanup(func() { hitInterval, writeInterval = oldHit, oldWrite })

	Record(path)
	Flush()
	Record(path)
	Flush()
	if r := row(t, path); r.Hits != 2 {
		t.Fatalf("hits = %d, want 2", r.Hits)
	}
}

// The same file is not written to the database more often than writeInterval, and
// what is buffered in the meantime is not lost: the hits are added on the next
// write.
func TestRepeatedReadsAreThrottled(t *testing.T) {
	prepare(t)
	const path = "/本机储存/热层/movie.mkv"

	Record(path)
	Flush()
	first := row(t, path)

	old := hitInterval
	hitInterval = 0
	t.Cleanup(func() { hitInterval = old })
	for i := 0; i < 5; i++ {
		Record(path)
	}
	Flush()
	if r := row(t, path); r.Hits != first.Hits {
		t.Fatalf("hits = %d, want %d: writes are throttled by %s", r.Hits, first.Hits, writeInterval)
	}

	// Let the interval pass: the buffered hits must not have been dropped.
	writeInterval = 0
	Flush()
	if r := row(t, path); r.Hits != first.Hits+5 {
		t.Fatalf("hits = %d, want %d", r.Hits, first.Hits+5)
	}
}

// A later read moves last_access forward and keeps first_access where it was.
func TestLastAccessMovesFirstAccessDoesNot(t *testing.T) {
	prepare(t)
	const path = "/本机储存/热层/movie.mkv"

	Record(path)
	Flush()
	first := row(t, path)

	time.Sleep(10 * time.Millisecond)
	writeInterval = 0
	Record(path)
	Flush()
	second := row(t, path)

	if !second.LastAccess.After(*first.LastAccess) {
		t.Fatalf("last_access did not move forward: %v -> %v", first.LastAccess, second.LastAccess)
	}
	if !second.FirstAccess.Equal(*first.FirstAccess) {
		t.Fatalf("first_access changed: %v -> %v", first.FirstAccess, second.FirstAccess)
	}
}

// The switch is on unless it is explicitly false.
func TestTrackingCanBeDisabled(t *testing.T) {
	prepare(t)
	setEnabled(t, "false")
	const path = "/本机储存/热层/movie.mkv"

	Record(path)
	Flush()
	if r := row(t, path); r != nil {
		t.Fatalf("disabled tracking wrote %+v", r)
	}
}

// Deleting a file drops what is buffered for it, so that a batch collected
// before the delete cannot insert the row again.
func TestForgetDropsPendingSample(t *testing.T) {
	prepare(t)
	const path = "/本机储存/热层/movie.mkv"

	Record(path)
	Forget(path)
	Flush()
	if r := row(t, path); r != nil {
		t.Fatalf("forgotten path was written: %+v", r)
	}
}

func TestPruneDropsOnlyStaleRows(t *testing.T) {
	// Rows and the in-memory tracker are package-global: without this the
	// survivor set depends on what ran before, so -shuffle turns it red.
	prepare(t)
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -400)
	fresh := now.Add(-time.Hour)
	for _, row := range []model.FileAccess{
		{Path: "/local/stale.bin", LastAccess: &old, FirstAccess: &old, Hits: 7},
		{Path: "/local/fresh.bin", LastAccess: &fresh, FirstAccess: &fresh, Hits: 3},
	} {
		if err := db.GetDb().Create(&row).Error; err != nil {
			t.Fatalf("insert %s: %v", row.Path, err)
		}
	}

	deleted, err := Prune(now.AddDate(0, 0, -365))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected to delete 1 stale row, deleted %d", deleted)
	}

	var paths []string
	if err := db.GetDb().Model(&model.FileAccess{}).Order("path").Pluck("path", &paths).Error; err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/local/fresh.bin" {
		t.Fatalf("wrong survivor set: %v", paths)
	}
}
