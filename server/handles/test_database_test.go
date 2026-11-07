package handles

import (
	"testing"

	"gorm.io/gorm"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
)

// useTestDatabase points the package-global database at a test-private one and
// puts the previous handle back when the test ends.
//
// The handles package shares a single database between all of its tests (see the
// init in offline_download_test.go). A test that calls db.Init for its own
// database and then closes that handle leaves the global pointing at a closed
// database, so every later test that relied on the shared one fails with
// "sql: database is closed". Which tests those are then depends on the order the
// tests happen to run in, and -shuffle (or any future reordering) turns the
// suite red for a reason that has nothing to do with the code under test.
func useTestDatabase(t *testing.T, database *gorm.DB) *gorm.DB {
	t.Helper()
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("database handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	previous := db.GetDb()
	db.Init(database)
	t.Cleanup(func() {
		db.Init(previous)
		_ = sqlDB.Close()
	})
	return database
}
