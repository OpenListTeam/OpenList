package s3

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/gorm/schema"
)

// withPrefixedDatabase runs the test against a database opened with the default
// table prefix (x_), which is what a shipped install uses.
func withPrefixedDatabase(t *testing.T) {
	t.Helper()
	original := db.GetDb()
	database, err := gorm.Open(sqlite.Open("file:prefixed_access?mode=memory&cache=shared"), &gorm.Config{
		Logger:         gormlogger.Default.LogMode(gormlogger.Silent),
		NamingStrategy: schema.NamingStrategy{TablePrefix: "x_"},
	})
	if err != nil {
		t.Fatalf("open prefixed database: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("database handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	db.Init(database)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		db.Init(original)
	})
}

// TestFlushAccess_WritesWithTablePrefix pins the flush against the configured
// table prefix. Spelling the table name out by hand produced "no such column"
// on every write once a prefix was configured, and the only symptom was an
// access list that stayed empty while the tiering job treated every object as
// never read.
func TestFlushAccess_WritesWithTablePrefix(t *testing.T) {
	withPrefixedDatabase(t)
	if got, err := db.TableNameOf(db.GetDb(), &model.S3ObjectMetadata{}); err != nil || got != "x_s3_object_metadata" {
		t.Fatalf("expected the prefixed table name, got %q (err %v)", got, err)
	}

	read := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	backend := &s3Backend{}
	backend.access.pending = map[string]time.Time{"/Movies/Hot/a.bin": read}
	backend.flushAccess()

	var rows []model.S3ObjectMetadata
	if err := db.GetDb().Find(&rows).Error; err != nil {
		t.Fatalf("read back access rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one access row, got %d", len(rows))
	}
	if rows[0].LastAccess == nil || !rows[0].LastAccess.Equal(read) {
		t.Fatalf("expected last_access %v, got %v", read, rows[0].LastAccess)
	}

	// A second flush that carries an older sample must not rewind the record.
	backend.access.written = nil
	backend.access.pending = map[string]time.Time{"/Movies/Hot/a.bin": read.Add(-time.Hour)}
	backend.flushAccess()
	rows = nil
	if err := db.GetDb().Find(&rows).Error; err != nil {
		t.Fatalf("read back access rows: %v", err)
	}
	if len(rows) != 1 || rows[0].LastAccess == nil || !rows[0].LastAccess.Equal(read) {
		t.Fatalf("expected last_access to stay at %v, got %+v", read, rows)
	}
}
