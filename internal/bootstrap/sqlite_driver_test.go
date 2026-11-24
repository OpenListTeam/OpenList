package bootstrap

import (
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestOpenSQLiteMigratesDeleteModeToWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data.db")

	// Simulate a legacy database created in DELETE rollback journal mode.
	legacyDB, err := gorm.Open(openSQLite(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.Exec("PRAGMA journal_mode=DELETE; CREATE TABLE t (id INTEGER PRIMARY KEY);").Error; err != nil {
		t.Fatal(err)
	}
	sqlLegacy, err := legacyDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	_ = sqlLegacy.Close()

	// Re-open with the standard bootstrap DSN and verify WAL + busy_timeout are active.
	dB, err := gorm.Open(openSQLite(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := dB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	var journalMode string
	if err := dB.Raw("PRAGMA journal_mode;").Scan(&journalMode).Error; err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := dB.Raw("PRAGMA busy_timeout;").Scan(&busyTimeout).Error; err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 10000 {
		t.Fatalf("busy_timeout = %d, want 10000", busyTimeout)
	}
}

func TestOpenSQLiteMemoryDSN(t *testing.T) {
	dB, err := gorm.Open(openSQLite("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := dB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	var probe int
	if err := dB.Raw("SELECT 1").Scan(&probe).Error; err != nil {
		t.Fatal(err)
	}
	if probe != 1 {
		t.Fatalf("probe = %d, want 1", probe)
	}
}
