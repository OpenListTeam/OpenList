package dbengine

import (
	"database/sql"
	"fmt"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"runtime"
	"strings"
)

// CreateSqliteCon uses best practices for sqlite and creates two connections
// One optimized for reading and the other optimized for writing
// found the information here: https://kerkour.com/sqlite-for-servers
// copied from https://github.com/bihe/monorepo/blob/477e534bd4c0814cdca73fea774b518148cebd3f/pkg/persistence/sqlite.go#L59
// with little edit.
// it should solve "database is locked error", and make better performance.
func CreateSqliteCon(dsn string, gormConfig *gorm.Config) (con model.Connection, err error) {
	var (
		read  *gorm.DB
		write *gorm.DB
	)

	// Read DB
	read, err = gorm.Open(sqlite.Open(dsn), gormConfig)
	if err != nil {
		return model.Connection{}, fmt.Errorf("cannot create read database connection: %w", err)
	}
	readDB, err := read.DB()
	if err != nil {
		return model.Connection{}, fmt.Errorf("cannot access underlying read database connection: %w", err)
	}
	if !strings.Contains(dsn, ":memory:") && !strings.Contains(dsn, "mode=memory") {
		err = setDefaultPragmas(readDB)
	}
	if err != nil {
		return model.Connection{}, err
	}
	readDB.SetMaxOpenConns(max(4, runtime.NumCPU())) // read in parallel with open connection per core

	// WriteDB
	write, err = gorm.Open(sqlite.Open(dsn), gormConfig)
	if err != nil {
		return model.Connection{}, fmt.Errorf("cannot create write database connection: %w", err)
	}
	writeDB, err := write.DB()
	if err != nil {
		return model.Connection{}, fmt.Errorf("cannot access underlying write database connection: %w", err)
	}
	if !strings.Contains(dsn, ":memory:") && !strings.Contains(dsn, "mode=memory") {
		err = setDefaultPragmas(writeDB)
	}
	if err != nil {
		return model.Connection{}, err
	}
	writeDB.SetMaxOpenConns(1) // only use one active connection for writing

	return model.Connection{
		Read:  read,
		Write: write,
	}, nil
}

// SetDefaultPragmas defines some sqlite pragmas for good performance and litestream compatibility
// https://highperformancesqlite.com/articles/sqlite-recommended-pragmas
// https://litestream.io/tips/
func setDefaultPragmas(db *sql.DB) error {
	var (
		stmt string
		val  string
	)
	defaultPragmas := map[string]string{
		"journal_mode": "wal",   // https://www.sqlite.org/pragma.html#pragma_journal_mode
		"busy_timeout": "5000",  // https://www.sqlite.org/pragma.html#pragma_busy_timeout
		"synchronous":  "1",     // NORMAL --> https://www.sqlite.org/pragma.html#pragma_synchronous
		"cache_size":   "10000", // 10000 pages = 40MB --> https://www.sqlite.org/pragma.html#pragma_cache_size
		"foreign_keys": "1",     // 1(bool) --> https://www.sqlite.org/pragma.html#pragma_foreign_keys
	}

	// set the pragmas
	for k := range defaultPragmas {
		stmt = fmt.Sprintf("pragma %s = %s", k, defaultPragmas[k])
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	// validate the pragmas
	for k := range defaultPragmas {
		row := db.QueryRow(fmt.Sprintf("pragma %s", k))
		err := row.Scan(&val)
		if err != nil {
			return err
		}
		if val != defaultPragmas[k] {
			return fmt.Errorf("could not set pragma %s to %s", k, defaultPragmas[k])
		}
	}

	return nil
}
