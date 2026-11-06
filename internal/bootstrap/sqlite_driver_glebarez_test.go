//go:build !sqlite_cgo_compat && !(linux && (mips || mips64 || mips64le || mipsle || loong64)) && !(windows && 386)

package bootstrap

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestNormalizeGlebarezSQLiteDSN(t *testing.T) {
	if got := normalizeGlebarezSQLiteDSN("file::memory:?cache=shared"); got != "file::memory:?cache=shared" {
		t.Fatalf("memory DSN changed: got %q", got)
	}

	got := normalizeGlebarezSQLiteDSN("data/data.db?_journal=WAL&_vacuum=incremental")
	for _, want := range []string{
		"_pragma=busy_timeout%2810000%29",
		"_pragma=journal_mode%28WAL%29",
		"_pragma=auto_vacuum%28incremental%29",
		"_txlock=immediate",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("normalized DSN %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "_journal=") || strings.Contains(got, "_vacuum=") {
		t.Fatalf("normalized DSN %q retained legacy mattn params", got)
	}
}

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
	dB, err := gorm.Open(openSQLite(fmt.Sprintf("%s?_journal=WAL&_vacuum=incremental", dbPath)), &gorm.Config{})
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
